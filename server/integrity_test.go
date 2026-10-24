package server

import (
	"context"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"net/http/httptest"
	"testing"
)

func approveIdentity(t *testing.T, h *httptest.Server, owner string, l kb.Library, sid, fid, base string) kb.Submission {
	t.Helper()
	op(t, h, owner, "submission.resolve", l.ID, map[string]string{"id": sid, "finding_id": fid, "base_revision": base, "rationale": "Compared the current claims, evidence, and applicability."}, 200)
	v := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": sid}, 200))
	return unpack[kb.Submission](t, op(t, h, owner, "submission.review", l.ID, map[string]any{"id": sid, "digest": v.Digest, "resolution_version": v.ResolutionVersion, "policy_version": l.PolicyVersion, "decision": "accept", "rationale": "Evidence supports this reviewed publication."}, 200))
}

func TestLostLedgerEditRequiresServerIdentityReview(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "lost-ledger", "private", "trusted")
	first := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	original := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	if original.FindingID == "" || original.Revision == "" {
		t.Fatal("server receipt lacks identity")
	}
	edit := document()
	edit.Markdown += "\nAdditional measurement qualification."
	revised := submit(t, h, owner, l.ID, id(), edit)
	runJob(t, s)
	revised = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": revised.ID}, 200))
	if revised.State != "needs_review" || revised.FindingID != original.FindingID || !revised.IdentityReview {
		t.Fatalf("lost identity minted new finding: %+v", revised)
	}
	op(t, h, owner, "submission.review", l.ID, map[string]any{"id": revised.ID, "digest": revised.Digest, "policy_version": l.PolicyVersion, "decision": "accept", "rationale": "Cannot bypass unresolved identity."}, 409)
	accepted := approveIdentity(t, h, owner, l, revised.ID, original.FindingID, original.Revision)
	if accepted.FindingID != original.FindingID || accepted.Revision == original.Revision {
		t.Fatal("revision lineage lost")
	}
	var count int
	s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.findings WHERE community_id=$1`, l.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate findings: %d", count)
	}
	// Same source in another explicit namespace is distinct, and ambiguity is reviewed.
	edit.SourceNamespace = "another-project"
	edit.Markdown += "\nDifferent source project."
	other := submit(t, h, owner, l.ID, id(), edit)
	runJob(t, s)
	other = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": other.ID}, 200))
	if other.State != "needs_review" {
		t.Fatal("similarity bypassed review")
	}
}

func TestExactCopiesAttachAndConcurrentNewSubmissionsConverge(t *testing.T) {
	s, h := fixture(t)
	owner, other := id(), id()
	l := create(t, h, owner, "exact-copies", "private", "trusted")
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
	op(t, h, other, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	a := submit(t, h, owner, l.ID, id(), document())
	renamed := document()
	renamed.SourcePath = "docs/renamed.md"
	b := submit(t, h, other, l.ID, id(), renamed)
	runJob(t, s)
	runJob(t, s)
	a = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": a.ID}, 200))
	b = unpack[kb.Submission](t, op(t, h, other, "submission.get", l.ID, map[string]string{"id": b.ID}, 200))
	if a.State != "accepted" || b.State != "accepted" || a.FindingID != b.FindingID || a.Revision != b.Revision || a.Author == b.Author {
		t.Fatalf("copies not attached with provenance: %+v %+v", a, b)
	}
	var sources int
	s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.publication_sources WHERE community_id=$1`, l.ID).Scan(&sources)
	if sources != 2 {
		t.Fatal("source aliases not registered")
	}
	// Revisions based on an old server receipt must not overwrite a newer revision.
	edit := document()
	edit.FindingID = a.FindingID
	edit.BaseRevision = a.Revision
	edit.Markdown += "\nFirst update."
	submit(t, h, owner, l.ID, id(), edit)
	runJob(t, s)
	edit.Markdown += "\nStale concurrent update."
	stale := submit(t, h, other, l.ID, id(), edit)
	runJob(t, s)
	stale = unpack[kb.Submission](t, op(t, h, other, "submission.get", l.ID, map[string]string{"id": stale.ID}, 200))
	if stale.State != "needs_review" && stale.State != "conflict" {
		t.Fatalf("stale update accepted: %+v", stale)
	}
}

func TestValidityAssessmentHistorySearchAndLocalCopySignals(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "false-claim", "private", "trusted")
	v := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	params := map[string]any{"id": v.FindingID, "revision": v.Revision, "status": "refuted", "reason": "A controlled measurement disproves the original claim.", "evidence": []kb.Evidence{{Label: "Corrected measurement", Excerpt: "The baseline measurement used a different time source."}}}
	op(t, h, id(), "finding.assess", l.ID, params, 404)
	op(t, h, owner, "finding.assess", l.ID, params, 200)
	op(t, h, owner, "finding.assess", l.ID, params, 409)
	got := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(got.Hits) != 0 {
		t.Fatal("refuted finding still retrieved as knowledge")
	}
	matches := unpack[[]kb.FindingMatch](t, op(t, h, owner, "finding.match", l.ID, map[string]any{"digests": []string{kb.TextDigest(document().Markdown)}}, 200))
	if len(matches) != 1 || matches[0].Status != "refuted" {
		t.Fatal("local copies cannot discover refutation")
	}
	history := unpack[[]kb.Change](t, op(t, h, owner, "finding.history", l.ID, map[string]string{"id": v.FindingID}, 200))
	if len(history) != 2 || history[0].Status != "refuted" || len(history[0].AssessmentEvidence) != 1 || history[1].Document.Markdown != document().Markdown {
		t.Fatalf("history lost: %+v", history)
	}
	legacy := unpack[kb.Changes](t, op(t, h, owner, "changes", l.ID, map[string]any{"since": 1}, 200))
	if len(legacy.Changes) != 1 || !legacy.Changes[0].Deleted {
		t.Fatal("legacy offline client would retain refuted content")
	}
	modern := unpack[kb.Changes](t, op(t, h, owner, "changes", l.ID, map[string]any{"since": 1, "integrity_version": 1}, 200))
	if len(modern.Changes) != 1 || modern.Changes[0].Deleted || modern.Changes[0].Status != "refuted" {
		t.Fatal("modern sync lost assessment")
	}
	params["revision"] = history[0].Revision
	params["status"] = "disputed"
	op(t, h, owner, "finding.assess", l.ID, params, 200)
	got = unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(got.Hits) != 1 || got.Hits[0].Status != "disputed" || len(got.Hits[0].Warnings) == 0 {
		t.Fatal("disputed finding not labeled")
	}
	latest := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": v.FindingID}, 200))
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": v.FindingID, "revision": latest[0].Revision, "reason": "Delete test finding"}, 200)
	matches = unpack[[]kb.FindingMatch](t, op(t, h, owner, "finding.match", l.ID, map[string]any{"digests": []string{kb.TextDigest(document().Markdown)}}, 200))
	if len(matches) != 0 {
		t.Fatal("deleted fingerprint retained")
	}
	var retained int
	s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.revisions WHERE community_id=$1 AND (text_digest<>'' OR reason<>'' OR assessment_evidence<>'[]')`, l.ID).Scan(&retained)
	if retained != 0 {
		t.Fatal("permanent deletion retained assessment content")
	}
}

func TestContributionDecisionIsAtomicAndReviewVersioned(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "appended-evidence", "private", "trusted")
	original := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	original = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": original.ID}, 200))
	d := document()
	d.SourcePath = "docs/independent-measurement.md"
	d.Markdown += "\nA second instrument confirms the same result."
	d.Evidence = []kb.Evidence{{Label: "Second instrument", Excerpt: "Independent measurements recorded the same ratio."}}
	v := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	op(t, h, owner, "submission.resolve", l.ID, map[string]string{"id": v.ID, "mode": "contribution", "finding_id": original.FindingID, "base_revision": original.Revision, "rationale": "Same claim and applicability, independently recorded evidence."}, 200)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	review := map[string]any{"id": v.ID, "digest": v.Digest, "policy_version": l.PolicyVersion, "decision": "accept", "rationale": "Accept the additional evidence."}
	op(t, h, owner, "submission.review", l.ID, review, 409)
	review["resolution_version"] = v.ResolutionVersion
	accepted := unpack[kb.Submission](t, op(t, h, owner, "submission.review", l.ID, review, 200))
	if accepted.FindingID == original.FindingID {
		t.Fatal("independent evidence overwrote canonical content")
	}
	found := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(found.Hits) != 1 || len(found.Hits[0].Contributions) != 1 {
		t.Fatalf("contribution not atomically grouped: %+v", found.Hits)
	}
	originalDoc := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": original.FindingID}, 200))
	if originalDoc[0].Document.Markdown != document().Markdown {
		t.Fatal("canonical content overwritten")
	}
	op(t, h, owner, "finding.assess", l.ID, map[string]any{"id": original.FindingID, "revision": original.Revision, "status": "refuted", "reason": "The shared assumption was disproven.", "evidence": document().Evidence}, 200)
	found = unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(found.Hits) != 1 || found.Hits[0].Status != "disputed" || len(found.Hits[0].Warnings) == 0 {
		t.Fatal("equivalent contribution remained unquestioned after refutation")
	}
	assessed := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": original.FindingID}, 200))[0]
	op(t, h, owner, "finding.assess", l.ID, map[string]any{"id": original.FindingID, "revision": assessed.Revision, "status": "active", "reason": "A follow-up experiment resolves the objection.", "evidence": document().Evidence}, 200)
	peer := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": accepted.FindingID}, 200))[0]
	if len(peer.Warnings) == 0 {
		t.Fatal("stale equivalence silently became valid after restoration")
	}
}

func TestReviewedGroupContributionsFollowCurrentRevisions(t *testing.T) {
	s, h := fixture(t)
	owner, outsider := id(), id()
	l := create(t, h, owner, "group-credit", "private", "trusted")
	claims := []kb.Submission{}
	for i := 0; i < 2; i++ {
		d := document()
		if i == 1 {
			d.SourcePath = "docs/alternate-clock.md"
			d.Markdown += "\nEquivalent clock wording."
		}
		v := submit(t, h, owner, l.ID, id(), d)
		runJob(t, s)
		v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
		if v.State != "accepted" {
			v = approveIdentity(t, h, owner, l, v.ID, "", "")
		}
		claims = append(claims, v)
	}
	a, b := claims[0], claims[1]
	op(t, h, owner, "finding.relate", l.ID, map[string]string{"source_id": b.FindingID, "source_revision": b.Revision, "target_id": a.FindingID, "target_revision": a.Revision, "kind": "equivalent", "rationale": "Same scoped assertion."}, 200)
	for _, f := range claims {
		credits := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": f.FindingID}, 200))
		if len(credits) != 2 {
			t.Fatalf("group member %s lost credit: %d", f.FindingID, len(credits))
		}
		last := credits[0]["submission_id"].(string)
		page := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": f.FindingID, "after": last}, 200))
		if len(page) != 1 || page[0]["submission_id"] != credits[1]["submission_id"] {
			t.Fatal("group credit pagination lost or repeated a contributor")
		}
		op(t, h, outsider, "finding.contributions", l.ID, map[string]string{"id": f.FindingID}, 404)
	}
	op(t, h, owner, "finding.assess", l.ID, map[string]string{"id": b.FindingID, "revision": b.Revision, "status": "disputed", "reason": "Applicability changed."}, 200)
	credits := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": a.FindingID}, 200))
	if len(credits) != 1 || credits[0]["submission_id"] != a.ID {
		t.Fatal("stale equivalence still aggregates contributions")
	}
}

func TestExactContributionVisibilityAndWorkerSourcePermissions(t *testing.T) {
	s, h := fixture(t)
	owner, other := id(), id()
	l := create(t, h, owner, "copy-provenance", "private", "trusted")
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
	op(t, h, other, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	a := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	submit(t, h, other, l.ID, id(), document())
	runJob(t, s)
	a = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": a.ID}, 200))
	contributions := unpack[[]map[string]any](t, op(t, h, other, "finding.contributions", l.ID, map[string]string{"id": a.FindingID}, 200))
	if len(contributions) != 2 {
		t.Fatal("accepted attribution hidden by submission RLS")
	}
	op(t, h, id(), "finding.contributions", l.ID, map[string]string{"id": a.FindingID}, 404)
	ctx := context.Background()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE re_community_worker`); err != nil {
		t.Fatal(err)
	}
	d := document()
	d.SourcePath = "docs/worker-alias.md"
	if err = bindSource(ctx, tx, l.ID, other, d, a.FindingID); err != nil {
		t.Fatalf("runtime worker cannot bind identity: %v", err)
	}
	tx.Rollback(ctx)
	// An exact attached copy has no separate content revision. Its identity
	// decision still belongs to the finding's permanent deletion boundary.
	for _, copy := range contributions {
		if _, err = s.DB.Exec(ctx, `INSERT INTO community.audit(community_id,actor_id,action,detail) VALUES($1,$2,'submission.resolve',$3)`, l.ID, owner, raw(map[string]any{"id": copy["submission_id"], "rationale": "Decision text must not survive deletion."})); err != nil {
			t.Fatal(err)
		}
	}
	current := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": a.FindingID}, 200))
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": a.FindingID, "revision": current[0].Revision, "reason": "Delete attached copies too."}, 200)
	var n int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM community.submissions WHERE community_id=$1`, l.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("attached copy survived permanent deletion")
	}
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM community.audit WHERE community_id=$1 AND action='submission.resolve'`, l.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("attached copy decision survived permanent deletion")
	}

}

func TestCorrectionCanReplaceRefutedContentWithoutRestoringItFirst(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "corrected-claim", "private", "trusted")
	v := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	op(t, h, owner, "finding.assess", l.ID, map[string]any{"id": v.FindingID, "revision": v.Revision, "status": "refuted", "reason": "Original measurement was invalid.", "evidence": document().Evidence}, 200)
	current := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": v.FindingID}, 200))[0]
	d := document()
	d.FindingID = v.FindingID
	d.BaseRevision = current.Revision
	d.Markdown += "\nCorrection: the original measurement was invalid; the new controlled experiment supports only the narrower condition."
	corrected := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	accepted := approveIdentity(t, h, owner, l, corrected.ID, v.FindingID, current.Revision)
	if accepted.State != "accepted" || accepted.FindingID != v.FindingID {
		t.Fatal("correction could not retain lineage")
	}
	history := unpack[[]kb.Change](t, op(t, h, owner, "finding.history", l.ID, map[string]string{"id": v.FindingID}, 200))
	if len(history) != 3 || history[0].Status != "active" || history[1].Status != "refuted" {
		t.Fatal("correction history lost")
	}
	matches := unpack[[]kb.FindingMatch](t, op(t, h, owner, "finding.match", l.ID, map[string]any{"digests": []string{kb.TextDigest(document().Markdown)}}, 200))
	if len(matches) != 1 || matches[0].Current {
		t.Fatal("old false local copy not identified as stale")
	}
}

func TestLegacyExactConsolidationPreservesIDs(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "legacy-copies", "private", "trusted")
	first := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	ctx := context.Background()
	fid, rid := id(), id()
	var seq int64
	if err := s.DB.QueryRow(ctx, `UPDATE community.libraries SET sequence=sequence+1 WHERE id=$1 RETURNING sequence`, l.ID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO community.findings(community_id,id,revision) VALUES($1,$2,$3)`, l.ID, fid, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id,content_digest,text_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, rid, l.ID, fid, seq, raw(document()), owner, kb.ContentDigest(document()), kb.TextDigest(document().Markdown)); err != nil {
		t.Fatal(err)
	}
	preview := unpack[map[string]any](t, op(t, h, owner, "finding.consolidate", l.ID, map[string]bool{"apply": false}, 200))
	if len(preview["pairs"].([]any)) != 1 {
		t.Fatal("legacy duplicate not detected")
	}
	op(t, h, owner, "finding.consolidate", l.ID, map[string]bool{"apply": true}, 200)
	result := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(result.Hits) != 1 || len(result.Hits[0].Contributions) != 1 {
		t.Fatal("legacy copies not grouped")
	}
	var count int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM community.findings WHERE community_id=$1`, l.ID).Scan(&count)
	if count != 2 {
		t.Fatal("consolidation deleted independent IDs")
	}
	replay := unpack[map[string]any](t, op(t, h, owner, "finding.consolidate", l.ID, map[string]bool{"apply": true}, 200))
	if len(replay["pairs"].([]any)) != 0 {
		t.Fatal("consolidation not idempotent")
	}
}

func TestServerSourceRegistryMatchesPortableVariantsAndSharedCheckouts(t *testing.T) {
	s, h := fixture(t)
	owner, reader := id(), id()
	l := create(t, h, owner, "source-variants", "private", "trusted")
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "reader"}, 200))
	op(t, h, reader, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	d := document()
	d.Markdown += "\n## Publication provenance\nPublished supporting evidence."
	v := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	for _, who := range []string{owner, reader} {
		matched := unpack[[]kb.FindingMatch](t, op(t, h, who, "finding.match", l.ID, map[string]any{"digests": []string{kb.TextDigest(document().Markdown)}, "source_paths": []string{d.SourcePath}}, 200))
		if len(matched) != 1 || matched[0].MatchKind != "source" || matched[0].FindingID != v.FindingID || matched[0].TextDigest == kb.TextDigest(document().Markdown) {
			t.Fatal("server source lineage not available for portable variant")
		}
	}
	scoped := unpack[[]kb.FindingMatch](t, op(t, h, reader, "finding.match", l.ID, map[string]any{"source_paths": []string{d.SourcePath}, "source_namespace": "different-project"}, 200))
	if len(scoped) != 0 {
		t.Fatal("source namespaces conflated")
	}
	op(t, h, id(), "finding.match", l.ID, map[string]any{"source_paths": []string{d.SourcePath}}, 404)
}
