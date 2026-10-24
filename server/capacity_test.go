package server

import (
	"context"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestReplayDoesNotSpendCapacity(t *testing.T) {
	_, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "replay", "private", "trusted")
	key := id()
	original := submit(t, h, owner, l.ID, key, document())
	t.Setenv("MAX_TOTAL_CONTENT_BYTES", "1")
	replay := submit(t, h, owner, l.ID, key, document())
	if replay.ID != original.ID {
		t.Fatal("retry did not return original submission")
	}
	reprepared := submit(t, h, owner, l.ID, id(), document())
	if reprepared.ID != original.ID {
		t.Fatal("identical preparation created new work")
	}
	changed := document()
	changed.Markdown += "\nA new observation."
	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": changed}, 429)
}

func TestWorkerCapacityFailureFinishesForReview(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "capacity", "private", "trusted")
	first := submit(t, h, owner, l.ID, id(), document())
	other := document()
	other.Markdown += "\nA second distinct finding."
	second := submit(t, h, owner, l.ID, id(), other)
	t.Setenv("MAX_COMMUNITY_FINDINGS", "1")
	runJob(t, s)
	runJob(t, s)
	accepted, pending := 0, 0
	for _, v := range []kb.Submission{first, second} {
		got := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
		if got.State == "accepted" {
			accepted++
		}
		if got.State == "needs_review" {
			pending++
		}
	}
	if accepted != 1 || pending != 1 {
		t.Fatalf("accepted=%d pending=%d", accepted, pending)
	}
	var active int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.jobs WHERE completed_at IS NULL`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("unfinished jobs=%d error=%v", active, err)
	}
}
