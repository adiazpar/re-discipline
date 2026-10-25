package community

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

const KnowledgeFormat = knowledge.KnowledgeFormat
const MaxKnowledgeBytes = 1 << 20
const MaxUploadBytes = 1536 << 10
const MaxUploadDocuments = 50

// KnowledgeDocument is the complete current document, without research,
// revisions, submissions, contributor identities, or review state.
type KnowledgeDocument struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Markdown    string `json:"markdown"`
	Fingerprint string `json:"fingerprint"`
}

type KnowledgeEntry struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	Sequence    int64  `json:"sequence"`
}

type KnowledgePage struct {
	Items    []KnowledgeEntry `json:"items"`
	Next     string           `json:"next,omitempty"`
	Sequence int64            `json:"sequence"`
}

type KnowledgeBatch struct {
	Key       string              `json:"key"`
	CreatedAt int64               `json:"created_at"`
	Documents []KnowledgeDocument `json:"documents"`
}

type KnowledgeReceipt struct {
	CommunityID string           `json:"community_id"`
	Key         string           `json:"key"`
	Items       []KnowledgeEntry `json:"items"`
	Updated     int              `json:"updated"`
	Unchanged   int              `json:"unchanged"`
	Sequence    int64            `json:"sequence"`
}

func normalizeKnowledge(raw string) string {
	return strings.ReplaceAll(strings.TrimPrefix(raw, "\ufeff"), "\r\n", "\n")
}
func FindingID(raw string) (string, error)         { return knowledge.FindingID(raw) }
func WithFindingID(raw, id string) (string, error) { return knowledge.WithFindingID(raw, id) }
func KnowledgeFingerprint(markdown string) string  { return knowledge.KnowledgeFingerprint(markdown) }

func PrepareKnowledge(rel, raw string) (KnowledgeDocument, error) {
	var d KnowledgeDocument
	if !utf8.ValidString(raw) {
		return d, fmt.Errorf("finding must be UTF-8 text")
	}
	if !strings.HasPrefix(rel, "docs/") || path.Clean(rel) != rel || strings.Contains(rel, "\\") || strings.HasPrefix(strings.ToLower(rel), "docs/ops/") || strings.EqualFold(rel, "docs/index.md") || !strings.EqualFold(path.Ext(rel), ".md") {
		return d, fmt.Errorf("select a finding under docs/, excluding ops and INDEX.md")
	}
	id, err := FindingID(raw)
	if err != nil || id == "" {
		return d, fmt.Errorf("finding needs a valid stable id: %v", err)
	}
	parsed := engine.Parse(rel, normalizeKnowledge(raw))
	if parsed.Status != "promoted" || (parsed.Kind != "fact" && parsed.Kind != "reference") {
		return d, fmt.Errorf("only promoted fact/reference findings are uploaded")
	}
	for _, line := range strings.Split(raw, "\n") {
		v := strings.ToLower(strings.TrimSpace(line))
		if v == "publish: false" || v == "audience: local" || v == "visibility: private" {
			return d, fmt.Errorf("finding explicitly excludes upload")
		}
	}
	projection := ProjectKnowledge(raw)
	d = KnowledgeDocument{ID: id, Path: rel, Markdown: projection.Markdown}
	if len(d.Markdown) > MaxKnowledgeBytes {
		return d, fmt.Errorf("publishable finding exceeds %d bytes", MaxKnowledgeBytes)
	}
	d.Fingerprint = KnowledgeFingerprint(d.Markdown)
	return d, nil
}

// ValidateKnowledge independently verifies the exact owner upload. The server
// never trusts a claimed fingerprint, and never accepts an evidence attachment.
func ValidateKnowledge(d KnowledgeDocument) error {
	prepared, err := PrepareKnowledge(d.Path, d.Markdown)
	if err != nil {
		return err
	}
	if prepared.ID != d.ID || prepared.Markdown != d.Markdown || prepared.Fingerprint != d.Fingerprint {
		return fmt.Errorf("document identity, fingerprint, or evidence-free content does not match")
	}
	return nil
}
