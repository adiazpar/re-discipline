package server

import (
	"context"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestBatchAllowanceIsolationPartialFailureAndReplay(t *testing.T) {
	s, h := fixture(t)
	owner, other := id(), id()
	l := create(t, h, owner, "seed-batch", "private", "trusted")
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
	op(t, h, other, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	t.Setenv("MAX_DAILY_SUBMISSIONS", "1")
	submit(t, h, owner, l.ID, id(), document())
	g := unpack[map[string]any](t, op(t, h, owner, "import.create", l.ID, map[string]any{"count": 2, "bytes": 100000, "hours": 1}, 200))
	grant := g["id"].(string)
	good := document()
	good.Markdown += "\nAn imported observation."
	bad := good
	bad.SourcePath = "docs/ops/private.md"
	last := good
	last.Markdown += "\nAnother distinct result."
	keys := []string{id(), id(), id()}
	items := []kb.BatchItem{{Key: keys[0], Document: good}, {Key: keys[1], Document: bad}, {Key: keys[2], Document: last}}
	var result struct {
		Items []kb.BatchOutcome `json:"items"`
	}
	result = unpack[struct {
		Items []kb.BatchOutcome `json:"items"`
	}](t, op(t, h, owner, "submission.batch", l.ID, map[string]any{"items": items, "import_grant": grant}, 200))
	if len(result.Items) != 3 || result.Items[0].Submission == nil || result.Items[1].Status != 422 || result.Items[2].Submission == nil {
		t.Fatalf("partial batch: %+v", result)
	}
	var remaining int
	if err := s.DB.QueryRow(context.Background(), `SELECT remaining_count FROM community.import_grants WHERE id=$1`, grant).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("allowance consumption: %d %v", remaining, err)
	}
	replay := unpack[struct {
		Items []kb.BatchOutcome `json:"items"`
	}](t, op(t, h, owner, "submission.batch", l.ID, map[string]any{"items": items[:1], "import_grant": grant}, 200))
	if replay.Items[0].Submission == nil || replay.Items[0].Submission.ID != result.Items[0].Submission.ID {
		t.Fatal("retry consumed exhausted allowance")
	}
	op(t, h, other, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": last, "import_grant": grant}, 429)
	op(t, h, other, "import.create", l.ID, map[string]any{"count": 1, "bytes": 1000, "hours": 1}, 403)
	foreign := create(t, h, owner, "different-community", "private", "trusted")
	op(t, h, owner, "submission.create", foreign.ID, map[string]any{"idempotency_key": id(), "document": last, "import_grant": grant}, 429)
	grants := unpack[[]map[string]any](t, op(t, h, owner, "import.list", l.ID, map[string]any{}, 200))
	if len(grants) != 1 {
		t.Fatal(grants)
	}
	for {
		worked, err := s.RunJob(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	for _, i := range []int{0, 2} {
		v := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": result.Items[i].Submission.ID}, 200))
		if v.State != "needs_review" {
			t.Fatalf("overlapping import bypassed identity review: %s", v.State)
		}
		approveIdentity(t, h, owner, l, v.ID, "", "")
	}
	var models int
	s.DB.QueryRow(context.Background(), `SELECT COALESCE(sum(count),0) FROM community.usage WHERE kind='model_review'`).Scan(&models)
	if models != 0 {
		t.Fatal("trusted batch spent model budget")
	}
}

func TestAllowanceExpiryRevocationAndPolicy(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "allowance-lifetime", "private", "trusted")
	g := unpack[map[string]any](t, op(t, h, owner, "import.create", l.ID, map[string]any{"count": 2, "bytes": 100000, "hours": 1}, 200))
	grant := g["id"].(string)
	op(t, h, owner, "import.create", l.ID, map[string]any{"count": 1, "bytes": 1000, "hours": 1}, 409)
	s.DB.Exec(context.Background(), `UPDATE community.import_grants SET expires_at=now()-interval '1 second' WHERE id=$1`, grant)
	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": document(), "import_grant": grant}, 429)
	g = unpack[map[string]any](t, op(t, h, owner, "import.create", l.ID, map[string]any{"count": 2, "bytes": 100000, "hours": 1}, 200))
	grant = g["id"].(string)
	op(t, h, owner, "import.revoke", l.ID, map[string]string{"id": grant}, 200)
	var retained int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.import_grants WHERE id=$1`, grant).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("revoked allowance retained: %d %v", retained, err)
	}

	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": document(), "import_grant": grant}, 429)
	l2 := create(t, h, owner, "review-required", "private", "maintainer")
	op(t, h, owner, "import.create", l2.ID, map[string]any{"count": 1, "bytes": 1000, "hours": 1}, 409)
}

func TestContributorEquivalencePreservesEvidenceAndInvalidatesOnEdit(t *testing.T) {
	s, h := fixture(t)
	owner, contributor := id(), id()
	l := create(t, h, owner, "equivalent-claims", "private", "trusted")
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
	op(t, h, contributor, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	a := document()
	b := document()
	b.Markdown += "\nIndependent confirmation of the timescale behavior."
	b.Evidence = []kb.Evidence{{Label: "Second investigator", Excerpt: "A separate measurement also recorded half the time step."}}
	sa := submit(t, h, owner, l.ID, id(), a)
	sb := submit(t, h, contributor, l.ID, id(), b)
	runJob(t, s)
	runJob(t, s)
	approveIdentity(t, h, owner, l, sb.ID, "", "")
	changes := unpack[kb.Changes](t, op(t, h, owner, "export", l.ID, map[string]any{}, 200))
	if len(changes.Changes) != 2 {
		t.Fatal("independent contributions were silently merged")
	}
	first, second := changes.Changes[0], changes.Changes[1]
	candidates := unpack[map[string]any](t, op(t, h, owner, "finding.candidates", l.ID, map[string]any{"document": b}, 200))
	if len(candidates["candidates"].([]any)) == 0 {
		t.Fatal("same identifier not suggested")
	}
	relation := map[string]any{"source_id": second.FindingID, "source_revision": second.Revision, "target_id": first.FindingID, "target_revision": first.Revision, "kind": "equivalent", "rationale": "Same build and conclusion; retain the independent measurements."}
	op(t, h, contributor, "finding.relate", l.ID, relation, 403)
	op(t, h, owner, "finding.relate", l.ID, relation, 200)
	query := unpack[struct {
		Hits []kb.Result `json:"hits"`
	}](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale", "limit": 1}, 200))
	if len(query.Hits) != 1 || len(query.Hits[0].Contributions) != 1 {
		t.Fatalf("equivalent search results: %+v", query)
	}
	for _, v := range []kb.Submission{sa, sb} {
		read := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
		if read.Author != v.Author || kb.Digest(read.Document) != kb.Digest(v.Document) {
			t.Fatal("contributor evidence or authorship was rewritten")
		}
	}
	edit := second.Document
	edit.FindingID = second.FindingID
	edit.BaseRevision = second.Revision
	edit.Markdown += "\nThe condition requires further qualification."
	submit(t, h, contributor, l.ID, id(), edit)
	runJob(t, s)
	relations := unpack[[]kb.Relation](t, op(t, h, owner, "finding.relations", l.ID, map[string]any{}, 200))
	if len(relations) != 1 || relations[0].Current {
		t.Fatal("equivalence survived an unreviewed edit")
	}
	op(t, h, owner, "finding.relate", l.ID, relation, 409)
	query = unpack[struct {
		Hits []kb.Result `json:"hits"`
	}](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(query.Hits) != 2 {
		t.Fatal("divergent revisions were hidden")
	}
	current := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": second.FindingID}, 200))[0]
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": second.FindingID, "revision": current.Revision, "reason": "Remove the imported test contribution."}, 200)
	relations = unpack[[]kb.Relation](t, op(t, h, owner, "finding.relations", l.ID, map[string]any{}, 200))
	if len(relations) != 0 {
		t.Fatal("deleted relation retained")
	}
}

func TestIdenticalDraftAliasesRemainIdempotentAndDeleted(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "draft-aliases", "private", "trusted")
	a, b := id(), id()
	original := submit(t, h, owner, l.ID, a, document())
	same := submit(t, h, owner, l.ID, b, document())
	if same.ID != original.ID {
		t.Fatal("repeat content duplicated")
	}
	changed := document()
	changed.Markdown += "\nDifferent content."
	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": b, "document": changed}, 409)
	runJob(t, s)
	changes := unpack[kb.Changes](t, op(t, h, owner, "export", l.ID, map[string]any{}, 200))
	v := changes.Changes[0]
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": v.FindingID, "revision": v.Revision, "reason": "Delete finding."}, 200)
	for _, key := range []string{a, b} {
		op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": key, "document": document()}, 410)
	}
}
