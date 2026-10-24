package server

import (
	"crypto/sha256"
	"fmt"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

// These are synthetic retrieval judgments, not claims about a real game. The
// oracle comes from the seeded documents, independently of the search results.
type loadTruth struct {
	finding, revision, textDigest, contentDigest, build, kind, grade string
}

type loadQuestion struct {
	name, query, kind, grade, answer string
	empty                            bool
}

func (q loadQuestion) data() map[string]any {
	return map[string]any{"query": q.query, "limit": 5, "kind": q.kind, "grade": q.grade, "assistance": false}
}

func (q loadQuestion) key() string { return string(raw(q.data())) }

type loadQueryResult struct {
	kb.QueryResult
	IndexedSequence int64 `json:"indexed_sequence"`
}

// Includes ordered IDs, revisions, snippets, provenance, warnings and sequence.
// The API does not expose baseline BM25 scores, so we do not claim to compare them.
func loadFingerprint(r loadQueryResult) [32]byte { return sha256.Sum256(raw(r)) }

func loadIntegrity(r loadQueryResult, community string, sequence int, truth map[string]loadTruth) bool {
	if r.IndexedSequence != int64(sequence) || len(r.Hits) > 5 || len(r.Warnings) != 0 || r.Assistance == nil || r.Assistance.Mode != "disabled" {
		return false
	}
	seen := map[string]bool{}
	for _, hit := range r.Hits {
		d, ok := truth[hit.FindingID]
		if !ok || seen[hit.FindingID] || hit.CommunityID != community || hit.Revision != d.revision || hit.TextDigest != d.textDigest || hit.Build != d.build || hit.Kind != d.kind || hit.Grade != d.grade || hit.Status != "active" || len(hit.Locations) != 1 {
			return false
		}
		seen[hit.FindingID] = true
		loc := hit.Locations[0]
		if loc.CommunityID != community || loc.FindingID != hit.FindingID || loc.Revision != d.revision {
			return false
		}
	}
	return true
}

func loadAnswerRank(q loadQuestion, r loadQueryResult) int {
	for i, hit := range r.Hits {
		if hit.FindingID == q.answer {
			return i + 1
		}
	}
	return 0
}

func loadAccuracyDocuments(ci int, build string) []kb.Document {
	makeDoc := func(name, kind, grade, idents, title, body string) kb.Document {
		return kb.Document{SourcePath: "docs/accuracy/" + name + ".md", Build: build, Kind: kind, Grade: grade,
			Markdown: fmt.Sprintf("---\nstatus: promoted\nkind: %s\ngrade: %s\nidents: [%s]\n---\n# %s\n%s\nloadcommunity%d\n", kind, grade, idents, title, body, ci)}
	}
	docs := []kb.Document{
		makeDoc("authority", "fact", "direct", "rd_tick_context", "Tick context ownership contract", strings.Repeat("The owner retains the context until the final callback returns. ", 300)),
		makeDoc("attachment", "fact", "direct", "", "Quaternion attachment transforms preserve joint orientation", "An attachment composes the parent joint quaternion with its local rotation to preserve orientation."),
		makeDoc("skinning", "fact", "inferred", "", "Quaternion skinning transforms", "Skinning blends quaternion transforms for weighted vertices. This is inferred from the animation trace."),
		makeDoc("reference", "reference", "direct", "rd_reference_frame", "Frame reference contract", "The declared frame counter names the current renderer frame."),
		makeDoc("debugger", "ops", "reported", "", "Debugger breakpoint mapping", "Map relocated module addresses before installing debugger breakpoints."),
		makeDoc("exclusive", "fact", "direct", "", "Community exclusive observation", fmt.Sprintf("exclusivecommunity%d", ci)),
	}
	// Exceed the engine's 100-candidate minimum with short incidental mentions.
	// An exact declaration must still win over the more numerous short matches.
	for i := 0; i < 120; i++ {
		docs = append(docs, makeDoc(fmt.Sprintf("mention-%d", i), "fact", "direct", "", "rd_tick_context side effect", "An incidental rd_tick_context mention."))
	}
	return docs
}

func loadAccuracyQuestions(ci int, docs []loadTruth) []loadQuestion {
	return []loadQuestion{
		{name: "exact_identifier", query: "rd_tick_context", answer: docs[0].finding},
		{name: "quoted_identifier", query: "`RD_TICK_CONTEXT`", answer: docs[0].finding},
		{name: "natural_language", query: "how do quaternion attachment transforms preserve joint orientation", answer: docs[1].finding},
		{name: "grade_filter", query: "quaternion transforms", grade: "inferred", answer: docs[2].finding},
		{name: "reference_filter", query: "rd_reference_frame", kind: "reference", answer: docs[3].finding},
		{name: "ops_filter", query: "debugger breakpoint mapping", kind: "ops", answer: docs[4].finding},
		{name: "absent", query: "zzabsentloadfixture", empty: true},
		{name: "other_community_only", query: fmt.Sprintf("exclusivecommunity%d", (ci+1)%4), empty: true},
		{name: "filter_excludes_answer", query: "rd_reference_frame", kind: "ops", empty: true},
	}
}

type loadScenario struct {
	name        string
	concurrency int
	mode        string
}

func loadCommunity(mode string, i int) int {
	switch mode {
	case "archive":
		return 1 + i%3
	case "baseline":
		return i % 2
	case "single", "single_reader", "reader_skewed":
		return 0
	case "skewed":
		if i%100 < 90 {
			return 0
		}
		return 1 + i%3
	default:
		return i % 4
	}
}

func loadSelectQuestion(mode string, i int, background []loadTruth, quality []loadQuestion) loadQuestion {
	if mode == "accuracy" || mode == "archive" {
		return quality[(i/4)%len(quality)]
	}
	if mode != "baseline" && i%3 != 0 {
		return loadQuestion{name: "unique_marker", query: fmt.Sprintf("loadmarker%d", i%len(background)), answer: background[i%len(background)].finding}
	}
	return loadQuestion{name: "broad", query: "timescale"}
}

type loadQuality struct {
	Requested          int     `json:"requested"`
	Completed          int     `json:"completed"`
	PositiveRequested  int     `json:"positive_requested"`
	PositiveCompleted  int     `json:"positive_completed"`
	Top1               int     `json:"top1_correct"`
	Top5               int     `json:"answer_in_top5"`
	ReciprocalRank     float64 `json:"reciprocal_rank_sum"`
	EmptyRequested     int     `json:"expected_empty_requested"`
	EmptyCompleted     int     `json:"expected_empty_completed"`
	EmptyCorrect       int     `json:"expected_empty_correct"`
	BaselineMismatches int     `json:"baseline_mismatches"`
}

func (v *loadQuality) observe(q loadQuestion, completed, same bool, rank int, empty bool) {
	v.Requested++
	if q.answer != "" {
		v.PositiveRequested++
	}
	if q.empty {
		v.EmptyRequested++
	}
	if !completed {
		return
	}
	v.Completed++
	if !same {
		v.BaselineMismatches++
	}
	if q.answer != "" {
		v.PositiveCompleted++
		if rank == 1 {
			v.Top1++
		}
		if rank > 0 {
			v.Top5++
			v.ReciprocalRank += 1 / float64(rank)
		}
	}
	if q.empty {
		v.EmptyCompleted++
		if empty {
			v.EmptyCorrect++
		}
	}
}

func (v loadQuality) correct() bool {
	return v.BaselineMismatches == 0 && v.Top1 == v.PositiveCompleted && v.EmptyCorrect == v.EmptyCompleted
}
