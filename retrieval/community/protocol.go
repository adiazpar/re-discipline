// Package community implements the community wire format and local client.
package community

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const ProtocolVersion = 1

type Request struct {
	Action    string          `json:"action"`
	Community string          `json:"community,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type Policy struct {
	Scope      string   `json:"scope"`
	Exclusions []string `json:"exclusions"`
	Mode       string   `json:"mode"` // maintainer, trusted, automated
}

type Library struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Visibility    string `json:"visibility"`
	Policy        Policy `json:"policy"`
	PolicyVersion int    `json:"policy_version"`
	Sequence      int64  `json:"sequence"`
	Role          string `json:"role,omitempty"`
}

type Evidence struct {
	Label       string `json:"label"`
	URL         string `json:"url,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

type Document struct {
	SourceNamespace string     `json:"source_namespace,omitempty"`
	FindingID       string     `json:"finding_id,omitempty"`
	BaseRevision    string     `json:"base_revision,omitempty"`
	SourcePath      string     `json:"source_path"`
	Markdown        string     `json:"markdown"`
	Kind            string     `json:"kind"`
	Grade           string     `json:"grade"`
	Build           string     `json:"build"`
	Evidence        []Evidence `json:"evidence"`
	Supersedes      string     `json:"supersedes,omitempty"`
}

type Submission struct {
	ResolutionVersion    int      `json:"resolution_version"`
	ContributionTarget   string   `json:"contribution_target,omitempty"`
	ContributionRevision string   `json:"contribution_revision,omitempty"`
	BaseRevision         string   `json:"base_revision,omitempty"`
	IdentityReview       bool     `json:"identity_review,omitempty"`
	FindingID            string   `json:"finding_id,omitempty"`
	Revision             string   `json:"revision,omitempty"`
	ID                   string   `json:"id"`
	CommunityID          string   `json:"community_id"`
	Author               string   `json:"author"`
	State                string   `json:"state"`
	Digest               string   `json:"digest"`
	Document             Document `json:"document"`
	Checks               []Check  `json:"checks"`
	PolicyVersion        int      `json:"policy_version"`
	CreatedAt            string   `json:"created_at"`
}

type Check struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}
type Change struct {
	Warnings           []string   `json:"warnings,omitempty"`
	AssessmentEvidence []Evidence `json:"assessment_evidence,omitempty"`
	Status             string     `json:"status,omitempty"`
	Reason             string     `json:"reason,omitempty"`
	ReplacementID      string     `json:"replacement_id,omitempty"`
	Author             string     `json:"author,omitempty"`
	Sequence           int64      `json:"sequence"`
	FindingID          string     `json:"finding_id"`
	Revision           string     `json:"revision"`
	Deleted            bool       `json:"deleted"`
	Document           Document   `json:"document"`
}
type Changes struct {
	Changes []Change `json:"changes"`
	Through int64    `json:"through"`
	More    bool     `json:"more"`
	Library Library  `json:"library"`
}

func Digest(d Document) string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var localPath = regexp.MustCompile(`(?i)(\b[a-z]:[\\/]|\\\\[a-z0-9]|/Users/|/home/|\blocalhost\b|127\.0\.0\.1|\[::1\])`)
var secret = regexp.MustCompile(`(?i)(-----BEGIN .*PRIVATE KEY-----|\b(?:sk-proj-|ghp_|github_pat_)[a-z0-9_-]{12,}|\b(?:api[_-]?key|password|access[_-]?token)\s*[:=]\s*["']?[a-z0-9_+/=-]{16,})`)

// Validate is a deterministic portability gate. Semantic scope review is separate.
func Validate(d Document) []Check {
	c := []Check{}
	add := func(code, msg string) { c = append(c, Check{code, msg, true}) }
	parsed := engine.Parse(d.SourcePath, d.Markdown)
	if parsed.Status != "promoted" {
		add("promotion", "Only locally promoted findings may be submitted.")
	}
	if parsed.Kind != "" && parsed.Kind != d.Kind {
		add("kind_mismatch", "Markdown kind differs from publication metadata.")
	}
	if parsed.Grade != "" && parsed.Grade != d.Grade {
		add("grade_mismatch", "Markdown grade differs from publication metadata.")
	}
	for _, line := range strings.Split(d.Markdown, "\n") {
		v := strings.ToLower(strings.TrimSpace(line))
		if v == "publish: false" || v == "audience: local" || v == "visibility: private" {
			add("private_marker", "Document explicitly excludes publication.")
			break
		}
	}
	p := strings.ToLower(strings.ReplaceAll(d.SourcePath, "\\", "/"))
	if !strings.HasPrefix(p, "docs/") || path.Clean(p) != p || p == "docs/index.md" || strings.HasPrefix(p, "docs/ops/") || !strings.HasSuffix(p, ".md") {
		add("source_path", "Select a Markdown finding under docs/, excluding ops and index files.")
	}
	if d.Kind != "fact" && d.Kind != "reference" {
		add("kind", "Only fact and reference findings can be published.")
	}
	if d.Grade != "direct" && d.Grade != "inferred" && d.Grade != "reported" {
		add("grade", "Declare direct, inferred, or reported evidence.")
	}
	if strings.TrimSpace(d.Build) == "" {
		add("build", "Identify the applicable software/build; use an explicit unknown when necessary.")
	}
	if len(d.Markdown) < 10 || len(d.Markdown) > 256*1024 {
		add("size", "Finding must contain 10 to 262144 bytes.")
	}
	// Inspect actual fields, not JSON escapes: a newline after "Notes:" and
	// the final "s:/" in an HTTPS URL are not Windows paths.
	fields := []string{d.SourceNamespace, d.SourcePath, d.Markdown, d.Build, d.Kind, d.Grade, d.Supersedes}
	for _, e := range d.Evidence {
		fields = append(fields, e.Label, e.URL, e.Excerpt)
	}
	material := strings.Join(fields, "\n")
	if localPath.MatchString(material) {
		add("local_environment", "Remove machine paths and local service addresses from the publication draft.")
	}
	if secret.MatchString(material) {
		add("secret", "Remove credential-like material from the publication draft.")
	}
	if len(d.Evidence) == 0 || len(d.Evidence) > 30 {
		add("evidence", "Provide 1 to 30 explicit evidence references or excerpts.")
	}
	for _, e := range d.Evidence {
		if strings.TrimSpace(e.Label) == "" || (e.Excerpt == "" && e.URL == "" && !e.Unavailable) {
			add("evidence", "Evidence needs a label and an excerpt, URL, or unavailable marker.")
		}
		if len(e.Excerpt) > 64*1024 {
			add("evidence_size", "Evidence excerpts must be at most 64 KiB.")
		}
		if e.URL != "" {
			u, err := url.Parse(e.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				add("evidence_url", "Evidence URLs must be HTTPS without embedded credentials.")
			}
		}
	}
	return c
}

func ValidateLibrary(l Library) error {
	if len(strings.TrimSpace(l.Name)) < 2 || len(l.Name) > 120 {
		return fmt.Errorf("name must contain 2 to 120 characters")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}$`).MatchString(l.Slug) {
		return fmt.Errorf("slug must contain 3 to 63 lowercase letters, digits, or hyphens")
	}
	if l.Visibility != "public" && l.Visibility != "unlisted" && l.Visibility != "private" {
		return fmt.Errorf("visibility must be public, unlisted, or private")
	}
	if len(strings.TrimSpace(l.Policy.Scope)) < 10 || len(l.Policy.Scope) > 8000 {
		return fmt.Errorf("scope must contain 10 to 8000 characters")
	}
	if l.Policy.Mode != "maintainer" && l.Policy.Mode != "trusted" && l.Policy.Mode != "automated" {
		return fmt.Errorf("review mode must be maintainer, trusted, or automated")
	}
	return nil
}
