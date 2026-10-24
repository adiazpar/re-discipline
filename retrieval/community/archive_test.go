package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ka "github.com/adiazpar/re-discipline/retrieval/knowledgearchive"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

func TestArchiveProgressAndLocalFailures(t *testing.T) {
	for _, scenario := range []string{"progress", "corrupt_partial", "short_partial", "corrupt_chunk", "existing_file", "invalid_destination"} {
		t.Run(scenario, func(t *testing.T) {
			cid, ticket := uuid.NewString(), strings.Repeat("t", 43)
			verified := false
			first := append(rawArchive(map[string]any{"type": "manifest", "data": map[string]any{"format": ka.Format, "community_id": cid, "name": "Knowledge", "scope": "fixture", "sequence": 0}}), '\n')
			firstChain := ka.Chain("", first)
			last := append(rawArchive(map[string]any{"type": "end", "data": map[string]any{"records": 0, "chain": firstChain}}), '\n')
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer operator" {
					t.Error("missing operator authentication")
				}
				var req Request
				json.NewDecoder(r.Body).Decode(&req)
				var p struct {
					Step, Ticket string
					Index        int64
				}
				json.Unmarshal(req.Data, &p)
				if req.Action == "community.delete" {
					if p.Step == "verify" {
						verified = true
						json.NewEncoder(w).Encode(map[string]any{"phase": "verified"})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"phase": "locked", "archive": true, "session_id": "fixture"})
					return
				}
				if req.Action != "community.archive.chunk" || p.Ticket != ticket {
					t.Fatal("unexpected operation")
				}
				b, prev, done := first, "", false
				if p.Index == 1 {
					b, prev, done = last, firstChain, true
				}
				encoder, _ := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(128<<10))
				compressed := encoder.EncodeAll(b, nil)
				encoder.Close()
				w.Header().Set("X-Archive-Next", strconv.FormatInt(p.Index+1, 10))
				w.Header().Set("X-Archive-Chain", ka.Chain(prev, b))
				w.Header().Set("X-Archive-Records", "0")
				w.Header().Set("X-Archive-Done", strconv.FormatBool(done))
				w.Header().Set("X-Archive-SHA256", ka.Digest(compressed))
				if scenario == "corrupt_chunk" {
					compressed[len(compressed)-1] ^= 1
				}
				w.Write(compressed)
			}))
			defer h.Close()
			c := &Client{URL: h.URL, Token: "operator", HTTP: h.Client()}
			path := filepath.Join(t.TempDir(), "knowledge.zst")
			if scenario == "existing_file" {
				os.WriteFile(path, []byte("preserve"), 0600)
			}
			if scenario == "invalid_destination" {
				os.Mkdir(path, 0700)
			}
			ctx := context.WithValue(context.Background(), archiveSliceKey{}, time.Now().Add(-time.Second))
			out, e := c.DownloadArchive(ctx, cid, ticket, path)
			resumeScenario := scenario == "progress" || scenario == "corrupt_partial" || scenario == "short_partial"
			if !resumeScenario {
				if e == nil || verified {
					t.Fatal("failed local transfer acknowledged", out, e)
				}
				if scenario == "existing_file" {
					b, _ := os.ReadFile(path)
					if string(b) != "preserve" {
						t.Fatal("existing file overwritten")
					}
				}
				return
			}
			if e != nil || verified || out.(map[string]any)["resume_required"] != true {
				t.Fatal(out, e, verified)
			}
			checkpoint, _ := os.ReadFile(path + ".state.json")
			if strings.Contains(string(checkpoint), ticket) {
				t.Fatal("raw workflow ticket persisted")
			}
			if scenario == "corrupt_partial" {
				f, err := os.OpenFile(path+".partial", os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteAt([]byte("damaged"), 0)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "short_partial" {
				if err := os.Truncate(path+".partial", 0); err != nil {
					t.Fatal(err)
				}
			}
			out, e = c.DownloadArchive(context.Background(), cid, ticket, path)
			if scenario != "progress" {
				if e == nil || verified {
					t.Fatal("damaged partial acknowledged", out, e)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("damaged archive published", err)
				}
				return
			}
			if e != nil || !verified || out.(map[string]any)["verified"] != true {
				t.Fatal(out, e, verified)
			}
		})
	}
}
