package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

// These opt-in tests use the real provider but only disposable local communities.
// Synthetic claims are test data, never research or production publications.
func TestJevTasksLive(t *testing.T) {
	if os.Getenv("RE_COMMUNITY_LIVE_JEV") != "1" {
		t.Skip("set RE_COMMUNITY_LIVE_JEV=1 to run provider-backed task validation")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" || semantic.APIKey() == "" {
		t.Fatal("live task validation requires a disposable PostgreSQL server and provider key")
	}
	results := []map[string]any{}
	defer func() {
		b, _ := json.MarshalIndent(map[string]any{"model": semantic.Model, "observed_at": time.Now().UTC(), "results": results, "passed": !t.Failed()}, "", "  ")
		if path := os.Getenv("RE_COMMUNITY_LIVE_REPORT"); path != "" {
			if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
		t.Log(string(b))
	}()
	for _, name := range []string{"publish_portable_claim", "exclude_unrelated_claim", "unknown_applicability"} {
		t.Run(name, func(t *testing.T) {
			s, h := fixture(t)
			s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
			owner := id()
			l := create(t, h, owner, "live-task", "private", "trusted")
			l.Policy.Assistance = true
			l.Policy.Mode = "automated"
			op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
			d := document()
			d.Evidence = nil
			want := "accepted"
			if name == "exclude_unrelated_claim" {
				d.Markdown = "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Bread dough rises with yeast\nBaker's yeast produces carbon dioxide in wheat bread dough. This claim concerns bread baking and has no connection to game engines."
				d.Build = "Wheat bread with baker's yeast at room temperature"
				want = "changes_requested"
			} else if name == "unknown_applicability" {
				d.Build = "TBD"
				want = "needs_review"
			}
			started := time.Now()
			key := id()
			v := submit(t, h, owner, l.ID, key, d)
			runJob(t, s)
			v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
			verified := v.State == want
			if want == "accepted" && verified {
				query := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale", "assistance": false}, 200))
				credits := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": v.FindingID}, 200))
				replay := submit(t, h, owner, l.ID, key, d)
				verified = len(query.Hits) == 1 && query.Hits[0].FindingID == v.FindingID && len(credits) == 1 && replay.ID == v.ID
			}
			results = append(results, map[string]any{"task": name, "expected": want, "actual": v.State, "verified": verified, "seconds": time.Since(started).Seconds()})
			if !verified {
				t.Errorf("task did not reach verified outcome: expected=%s actual=%s", want, v.State)
			}
		})
	}
	t.Run("complete_duplicate_health_task", func(t *testing.T) {
		s, h := fixture(t)
		owner := id()
		l := create(t, h, owner, "health-task", "private", "trusted")
		d := document()
		d.Evidence = nil
		first := submit(t, h, owner, l.ID, id(), d)
		runJob(t, s)
		first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
		d.SourcePath = "docs/clock-paraphrase.md"
		d.Markdown = "---\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [timescale]\n---\n# The timescale variable controls engine time\nSetting timescale to 0.5 makes the engine time step half its baseline value."
		second := submit(t, h, owner, l.ID, id(), d)
		runJob(t, s)
		second = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": second.ID}, 200))
		if second.State != "accepted" {
			second = approveIdentity(t, h, owner, l, second.ID, "", "")
		}
		l.Policy.Assistance = true
		op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
		s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
		started := time.Now()
		if worked, err := s.RunHealthJob(context.Background()); err != nil || !worked {
			t.Fatalf("health failed: %v %v", worked, err)
		}
		issues := unpack[struct {
			Items []healthIssue `json:"items"`
		}](t, op(t, h, owner, "health.list", l.ID, map[string]any{}, 200))
		verified := len(issues.Items) == 1 && issues.Items[0].Kind == "duplicate"
		if verified {
			op(t, h, owner, "health.review", l.ID, map[string]string{"id": issues.Items[0].ID, "decision": "combine", "rationale": "Authorized fixture task: equivalent engine-clock assertions and identical applicability; preserve both contributions."}, 200)
			query := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale", "assistance": false}, 200))
			verified = len(query.Hits) == 1
			if verified {
				credits := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": query.Hits[0].FindingID}, 200))
				verified = len(credits) == 2
			}
		}
		results = append(results, map[string]any{"task": "complete_duplicate_health_task", "verified": verified, "issues": issues.Items, "seconds": time.Since(started).Seconds()})
		if !verified {
			t.Error("health task did not produce one reader entry retaining both contributions")
		}
	})
	t.Run("semantic_boundaries", func(t *testing.T) {
		client := semantic.New(semantic.Config{Enabled: true}, t.TempDir())
		left := kb.ProjectClaim(document(), "active")
		for _, name := range []string{"changed_number", "negation", "different_build", "partial_overlap", "unknown_scope", "truncated"} {
			right := left
			right.Idents = append([]string{}, left.Idents...)
			want := "conflicting"
			switch name {
			case "changed_number":
				right.Body = strings.ReplaceAll(right.Body, "halves", "doubles")
			case "negation":
				right.Body = strings.ReplaceAll(right.Body, "halves", "does not change")
			case "different_build":
				right.Build = "DOOM Eternal build 1, not DOOM 2016"
				want = "separate"
			case "partial_overlap":
				right.Body += "\nThe timescale console variable can only be changed while the console is unlocked."
				want = "partial"
			case "unknown_scope":
				right.Build = "TBD"
				want = "unknown"
			case "truncated":
				right.Truncated = true
				want = "unknown"
			}
			j, err := client.Evaluate(context.Background(), "task-boundaries", map[string]any{"left": left, "right": right}, semantic.CompareQuestions(), false)
			if err != nil {
				t.Fatal(err)
			}
			actual := semantic.Relation(j, left, right)
			// Abstention is a safe exception, not a correct classification or a
			// completed task. Retain both metrics so semantic misses stay visible.
			safe := actual == want || actual == "unknown"
			results = append(results, map[string]any{"task": name, "expected": want, "actual": actual, "classified_as_expected": actual == want, "handled_safely": safe, "judgment": j})
			if !safe {
				t.Errorf("%s made an unsafe decision: expected %s got %s", name, want, actual)
			}
		}
	})
}
