package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestInvitationDeletionAndIsolation(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	owner, other := id(), id()
	l := create(t, h, owner, "invite-deletion", "private", "trusted")
	invite := unpack[map[string]string](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "contributor"}, 200))
	op(t, h, other, "invite.revoke", l.ID, map[string]string{"id": invite["id"]}, 404)
	op(t, h, owner, "invite.revoke", l.ID, map[string]string{"id": invite["id"]}, 200)
	op(t, h, other, "invite.redeem", "", map[string]string{"token": invite["token"]}, 404)
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.invites WHERE id=$1`, invite["id"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked invitation retained: %d %v", count, err)
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.audit WHERE detail->>'invite_id'=$1 OR detail->>'id'=$1`, invite["id"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invitation history retained: %d %v", count, err)
	}
	invite = unpack[map[string]string](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "contributor"}, 200))
	op(t, h, other, "invite.redeem", "", map[string]string{"token": invite["token"]}, 200)
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.invites WHERE id=$1`, invite["id"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("redeemed invitation retained: %d %v", count, err)
	}
	invite = unpack[map[string]string](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "reader"}, 200))
	if _, err := s.DB.Exec(ctx, `UPDATE community.invites SET expires_at=now()-interval '1 second' WHERE id=$1`, invite["id"]); err != nil {
		t.Fatal(err)
	}
	if got := unpack[[]any](t, op(t, h, owner, "invite.list", l.ID, map[string]any{}, 200)); len(got) != 0 {
		t.Fatal("inactive invitations listed", got)
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.invites WHERE community_id=$1`, l.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired invitation retained: %d %v", count, err)
	}
	op(t, h, owner, "member.remove", l.ID, map[string]string{"user_id": other}, 200)
	op(t, h, other, "community.get", l.ID, map[string]any{}, 404)
}

func TestFindingDeletionErasesContentAndCannotReplay(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	owner, stranger := id(), id()
	l := create(t, h, owner, "finding-deletion", "public", "trusted")
	key := id()
	submission := submit(t, h, owner, l.ID, key, document())
	runJob(t, s)
	page := unpack[kb.Changes](t, op(t, h, owner, "changes", l.ID, map[string]any{}, 200))
	f := page.Changes[0]
	op(t, h, owner, "query", l.ID, map[string]string{"query": "timescale"}, 200)
	op(t, h, stranger, "finding.withdraw", l.ID, map[string]string{"id": f.FindingID, "revision": f.Revision, "reason": "Remove"}, 403)
	tx, err := s.begin(ctx, stranger)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `SELECT community.erase_finding($1,$2)`, l.ID, f.FindingID)
	tx.Rollback(ctx)
	if err == nil {
		t.Fatal("erasure function bypassed membership")
	}
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": f.FindingID, "revision": f.Revision, "reason": "Remove this content"}, 200)
	for _, table := range []string{"findings", "submissions", "reviews", "jobs"} {
		var count int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.`+table+` WHERE community_id=$1`, l.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained deleted content: %d %v", table, count, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Config.CacheDir, l.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted search cache remains: %v", err)
	}
	op(t, h, owner, "submission.get", l.ID, map[string]string{"id": submission.ID}, 404)
	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": key, "document": document()}, 410)
	for _, request := range []map[string]any{{}, {"through": 1}, {"since": 1}} {
		changes := unpack[kb.Changes](t, op(t, h, "", "changes", l.ID, request, 200))
		if changes.More || len(changes.Changes) == 0 {
			t.Fatal("deletion broke snapshot pagination", changes)
		}
		for _, change := range changes.Changes {
			if !change.Deleted || change.Document.Markdown != "" || len(change.Document.Evidence) != 0 {
				t.Fatal("deleted content in synchronization", change)
			}
		}
	}
	result := unpack[struct {
		Hits []any `json:"hits"`
	}](t, op(t, h, "", "query", l.ID, map[string]string{"query": "timescale"}, 200))
	if len(result.Hits) != 0 {
		t.Fatal("search resurrected deleted finding")
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.audit WHERE community_id=$1 AND action LIKE 'submission.%'`, l.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("publication history retained: %d %v", count, err)
	}
}
