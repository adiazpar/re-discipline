package server

import (
	"context"
	"encoding/json"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/server/maintenance"
)

func TestCleanupPreservesHistoryCreditAssessmentAndRejectsStaleHead(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "cleanup", "private", "trusted")
	v := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	op(t, h, owner, "finding.assess", l.ID, map[string]string{"id": v.FindingID, "revision": v.Revision, "status": "disputed", "reason": "Applicability remains uncertain."}, 200)
	ctx := context.Background()
	var before []byte
	var base string
	if err := s.DB.QueryRow(ctx, `SELECT r.id::text,r.document FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.id::text=$1`, v.FindingID).Scan(&base, &before); err != nil {
		t.Fatal(err)
	}
	var d kb.Document
	json.Unmarshal(before, &d)
	d.Evidence = nil
	item := maintenance.CleanupItem{FindingID: v.FindingID, Base: base, Revision: id(), Status: "disputed", Before: before, After: raw(d)}
	plan := maintenance.CleanupPlan{Community: l.ID, Items: []maintenance.CleanupItem{item}}
	parts, err := maintenance.CleanupSQL(plan, 100)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if _, err = s.DB.Exec(ctx, parts[0]); err != nil {
			t.Fatal(err)
		}
	}
	var status, author, reason string
	var revisions, credits, audits int
	err = s.DB.QueryRow(ctx, `SELECT r.status,r.author_id::text,r.reason,(SELECT count(*) FROM community.revisions WHERE finding_id=f.id),(SELECT count(*) FROM community.submissions WHERE finding_id=f.id::text AND state='accepted'),(SELECT count(*) FROM community.audit WHERE action='finding.cleanup' AND detail->>'id'=f.id::text) FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.id::text=$1`, v.FindingID).Scan(&status, &author, &reason, &revisions, &credits, &audits)
	if err != nil || status != "disputed" || author != owner || reason != "Applicability remains uncertain." || revisions != 3 || credits != 1 || audits != 1 {
		t.Fatalf("cleanup changed history or assessment: %s %s %s %d %d %d %v", status, author, reason, revisions, credits, audits, err)
	}
	plan.Items[0].Revision = id()
	parts, err = maintenance.CleanupSQL(plan, 100)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := s.DB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, parts[0]); err == nil {
		t.Fatal("stale cleanup succeeded")
	}
	conn.Exec(ctx, "ROLLBACK")
	plan.Items[0].After = raw(kb.Document{Build: "silently changed build", Markdown: d.Markdown, Kind: d.Kind, Grade: d.Grade, SourcePath: d.SourcePath})
	if _, err = maintenance.CleanupSQL(plan, 100); err == nil {
		t.Fatal("cleanup changed applicability")
	}
}
