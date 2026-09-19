// Package semantic supplies optional, typed advisory judgments. Callers own policy.
package semantic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const Model = "jev-1.13.0"
const Endpoint = "https://api.typesafe.ai/v1/systemone"
const Rubric = "claims-2026-09-19"

type Config struct {
	Enabled        bool `json:"enabled"`
	Candidates     int  `json:"candidates,omitempty"`
	DailyRequests  int  `json:"daily_requests,omitempty"`
	TimeoutSeconds int  `json:"timeout_seconds,omitempty"`
}

func (c Config) Normalized() (Config, error) {
	if c.Candidates == 0 {
		c.Candidates = 32
	}
	if c.DailyRequests == 0 {
		c.DailyRequests = 200
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 8
	}
	if c.Candidates < 8 || c.Candidates > 128 || c.DailyRequests < 1 || c.DailyRequests > 100000 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 {
		return c, errors.New("assistance limits: candidates 8–128, daily_requests 1–100000, timeout_seconds 1–60")
	}
	return c, nil
}

func ConfigPath(root string) string { return filepath.Join(root, ".re-discipline", "assistance.json") }
func LoadConfig(root string) (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return c.Normalized()
	}
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid assistance configuration: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("invalid trailing assistance configuration")
	}
	return c.Normalized()
}

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}
type Usage struct {
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}
type Judgment struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
	Cached  bool              `json:"cached"`
}
type Client struct {
	Config   Config
	CacheDir string
	HTTP     *http.Client
	Key      func() string
	Reserve  func(context.Context) error
}

func New(c Config, cacheDir string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{Config: c, CacheDir: cacheDir, Key: APIKey, HTTP: &http.Client{
		Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func probability(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

func Validate(j Judgment, questions map[string]Question) error {
	invalid := errors.New("invalid typed response; judgment unavailable")
	if j.Model != Model || len(j.Answers) != len(questions) {
		return invalid
	}
	for name, q := range questions {
		a, ok := j.Answers[name]
		if !ok || a.Type != q.Type {
			return invalid
		}
		switch q.Type {
		case "noul":
			if a.Noul == nil || !probability(*a.Noul) {
				return invalid
			}
		case "choice":
			if _, ok := q.Criteria[a.Choice]; !ok || a.Confidence == nil || !probability(*a.Confidence) || len(a.Probabilities) != len(q.Criteria) {
				return invalid
			}
			total, best := 0.0, 0.0
			for label := range q.Criteria {
				p, ok := a.Probabilities[label]
				if !ok || !probability(p) {
					return invalid
				}
				total += p
				if p > best {
					best = p
				}
			}
			if math.Abs(total-1) > .01 || a.Probabilities[a.Choice] < best {
				return invalid
			}
		default:
			return invalid
		}
	}
	if (j.Usage.InputTokens != nil && *j.Usage.InputTokens < 0) || (j.Usage.OutputTokens != nil && *j.Usage.OutputTokens < 0) {
		return invalid
	}
	return nil
}

func (c *Client) cache() (*sql.DB, error) {
	if c.CacheDir == "" {
		return nil, errors.New("assistance requires a private cache directory for budget accounting")
	}
	if err := os.MkdirAll(c.CacheDir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(c.CacheDir, "judgments.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS judgments(key TEXT PRIMARY KEY,value TEXT NOT NULL,used_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS budgets(day TEXT PRIMARY KEY,calls INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS cache_epoch(id INTEGER PRIMARY KEY,generation INTEGER NOT NULL); INSERT OR IGNORE INTO cache_epoch(id,generation) VALUES(1,0);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Evaluate never retries, follows redirects, or calls a provider when disabled/offline.
// Namespace separates private communities/projects even for identical inputs.
func (c *Client) Evaluate(ctx context.Context, namespace string, state any, questions map[string]Question, offline bool) (Judgment, error) {
	var result Judgment
	config, err := c.Config.Normalized()
	if err != nil {
		return result, err
	}
	if !config.Enabled {
		return result, errors.New("optional assistance is disabled")
	}
	if len(questions) == 0 || len(questions) > 16 {
		return result, errors.New("select 1–16 typed questions")
	}
	for _, q := range questions {
		if q.Type != "noul" && q.Type != "choice" {
			return result, errors.New("unsupported question type")
		}
	}
	request := map[string]any{"model": Model, "state": state, "questions": questions}
	payload, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	if len(payload) > 128*1024 {
		return result, errors.New("assistance projection exceeds 128 KiB; narrow the comparison")
	}
	db, err := c.cache()
	if err != nil {
		return result, errors.New("assistance cache unavailable; using ordinary workflow")
	}
	defer db.Close()
	var epoch int64
	if err = db.QueryRowContext(ctx, `SELECT generation FROM cache_epoch WHERE id=1`).Scan(&epoch); err != nil {
		return result, err
	}
	key := Hash([]any{Rubric, namespace, epoch, request})
	var saved string
	if db.QueryRowContext(ctx, `SELECT value FROM judgments WHERE key=?`, key).Scan(&saved) == nil && json.Unmarshal([]byte(saved), &result) == nil && Validate(result, questions) == nil {
		result.Cached = true
		db.ExecContext(ctx, `UPDATE judgments SET used_at=? WHERE key=?`, time.Now().UTC().Format(time.RFC3339Nano), key)
		return result, nil
	}
	result = Judgment{}
	if offline {
		return result, errors.New("no cached judgment; offline mode makes no provider requests")
	}
	keyValue := strings.TrimSpace(c.Key())
	if keyValue == "" {
		return result, errors.New("TYPESAFE_API_KEY is not configured; using ordinary workflow")
	}
	day := time.Now().UTC().Format("2006-01-02")
	if _, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO budgets(day,calls) VALUES(?,0)`, day); err != nil {
		return result, errors.New("assistance budget unavailable")
	}
	var count int
	err = db.QueryRowContext(ctx, `UPDATE budgets SET calls=calls+1 WHERE day=? AND calls<? RETURNING calls`, day, config.DailyRequests).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return result, errors.New("daily assistance budget exhausted; using ordinary workflow")
	}
	if err != nil {
		return result, errors.New("assistance budget unavailable")
	}
	if c.Reserve != nil {
		if err = c.Reserve(ctx); err != nil {
			return result, errors.New("service assistance budget exhausted; using ordinary workflow")
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, "POST", Endpoint, bytes.NewReader(payload))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+keyValue)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return result, errors.New("assistance request unavailable; using ordinary workflow")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, fmt.Errorf("assistance returned HTTP %d; using ordinary workflow", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(b) > 128*1024 {
		return result, errors.New("invalid assistance response size")
	}
	if json.Unmarshal(b, &result) != nil {
		return result, errors.New("invalid assistance JSON")
	}
	if err = Validate(result, questions); err != nil {
		return Judgment{}, err
	}
	result.Cached = false
	encoded, _ := json.Marshal(result)
	_, err = db.ExecContext(ctx, `INSERT INTO judgments(key,value,used_at) SELECT ?,?,? FROM cache_epoch WHERE id=1 AND generation=? ON CONFLICT(key) DO UPDATE SET value=excluded.value,used_at=excluded.used_at`, key, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), epoch)
	if err != nil {
		return Judgment{}, errors.New("could not retain assistance judgment")
	}
	db.ExecContext(ctx, `DELETE FROM judgments WHERE key IN (SELECT key FROM judgments ORDER BY used_at DESC LIMIT -1 OFFSET 20000); DELETE FROM budgets WHERE day < date('now','-7 days')`)
	return result, nil
}

// Purge also invalidates in-flight writes. Budgets survive cache erasure.
func (c *Client) Purge(ctx context.Context) error {
	db, err := c.cache()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE cache_epoch SET generation=generation+1 WHERE id=1; DELETE FROM judgments`); err != nil {
		return err
	}
	return tx.Commit()
}
