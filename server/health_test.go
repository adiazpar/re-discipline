package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthRunsAsRestrictedProductionWorker(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	owner := id()
	l := create(t, h, owner, "restricted-health", "private", "trusted")
	d := document()
	first := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	d.SourcePath = "docs/alternate.md"
	d.Markdown += "\nThis describes the simulation clock."
	second := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	second = approveIdentity(t, h, owner, l, second.ID, "", "")
	op(t, h, owner, "finding.relate", l.ID, map[string]string{"source_id": second.FindingID, "source_revision": second.Revision, "target_id": first.FindingID, "target_revision": first.Revision, "kind": "equivalent", "rationale": "The same scoped clock behavior."}, 200)
	config := s.DB.Config()
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE re_community_worker`)
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worker := New(db, Config{CacheDir: t.TempDir()})
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM community.finding_relations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("private relationship not visible to worker: %d %v", count, err)
	}
	if worked, e := worker.RunHealthJob(ctx); e != nil || !worked {
		t.Fatalf("production-role health did not advance: %v %v", worked, e)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM community.health_work WHERE analyzed_version=version`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("worker did not complete a subject: %d %v", count, err)
	}
	if _, err = db.Exec(ctx, `UPDATE community.finding_relations SET rationale=rationale WHERE false`); err == nil {
		t.Fatal("health worker gained relationship mutation authority")
	}
}

func TestHealthDoesNotSpendRequestsOnUnusableComparisons(t *testing.T) {
	for _, scenario := range []string{"unknown_scope", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			s, h := fixture(t)
			owner := id()
			l := create(t, h, owner, "no-waste", "private", "maintainer")
			l.Policy.Assistance = true
			op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
			d := document()
			if scenario == "unknown_scope" {
				d.Build = "TBD"
			} else {
				d.Markdown += strings.Repeat(" Additional clock details.", 400)
			}
			submit(t, h, owner, l.ID, id(), d)
			d.SourcePath = "docs/other-clock.md"
			d.Markdown += "\nClock relation under additional conditions."
			submit(t, h, owner, l.ID, id(), d)
			s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
			s.Semantic.Key = func() string { return "test-key" }
			var calls atomic.Int32
			s.Semantic.HTTP = &http.Client{Transport: healthTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, fmt.Errorf("unexpected provider request")
			})}
			if worked, err := s.RunHealthJob(context.Background()); err != nil || !worked {
				t.Fatalf("health failed: %v %v", worked, err)
			}
			if calls.Load() != 0 {
				t.Fatal("spent provider requests on judgments that must be discarded")
			}
			var incomplete int
			if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.health_work WHERE last_error LIKE 'Comparison needs%'`).Scan(&incomplete); err != nil || incomplete == 0 {
				t.Fatalf("unavailable comparison was not reported: %d %v", incomplete, err)
			}
		})
	}
}

func TestClaimCopiesDoNotSwallowExplicitCorrections(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "grade-correction", "private", "trusted")
	d := document()
	d.Evidence = nil
	first := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	d.FindingID = first.FindingID
	d.BaseRevision = first.Revision
	d.Grade = "reported"
	d.Markdown = strings.Replace(d.Markdown, "grade: direct", "grade: reported", 1)
	second := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	second = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": second.ID}, 200))
	if second.State != "accepted" || second.Revision == first.Revision {
		t.Fatal("explicit correction silently attached as a copy")
	}
	current := unpack[[]kb.Change](t, op(t, h, owner, "finding.get", l.ID, map[string]string{"id": first.FindingID}, 200))
	if current[0].Document.Grade != "reported" {
		t.Fatal("accepted correction not applied")
	}
	op(t, h, owner, "finding.withdraw", l.ID, map[string]string{"id": first.FindingID, "revision": second.Revision, "reason": "Remove this claim and its fingerprints."}, 200)
	var count int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.revisions WHERE community_id=$1 AND claim_digest<>''`, l.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("erasure retained fingerprint: %d %v", count, err)
	}
}

func TestSearchHonorsAssistanceDisabledWhileWaitingForIndex(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "disable-waiting", "private", "trusted")
	submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	l.Policy.Assistance = true
	l = unpack[kb.Library](t, op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200))
	s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	s.Semantic.Key = func() string { return "test-key" }
	var calls atomic.Int32
	s.Semantic.HTTP = &http.Client{Transport: healthTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("provider must remain unused")
	})}
	tx, err := s.begin(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	releaseIndex, lockErr := s.lockIndex(context.Background(), l.ID, true)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	done := make(chan error, 1)
	go func() {
		_, e := s.query(context.Background(), tx, l, raw(map[string]any{"query": "Why is the engine time step halved?"}))
		done <- e
	}()
	disabled := l.Policy
	disabled.Assistance = false
	op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": disabled, "expected_version": l.PolicyVersion}, 200)
	releaseIndex()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("disabled community content was sent to provider")
	}
}

func TestAutomaticAllowanceDoesNotBlockOrdinaryPublisherQuota(t *testing.T) {
	s, h := fixture(t)
	owner, publisher := id(), id()
	l := create(t, h, owner, "allowance-fallback", "private", "trusted")
	invite := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
	op(t, h, publisher, "invite.redeem", "", map[string]any{"token": invite["token"]}, 200)
	grant := unpack[map[string]any](t, op(t, h, owner, "import.create", l.ID, map[string]any{"user_id": publisher, "count": 1, "bytes": 1, "hours": 1}, 200))
	s.DB.Exec(context.Background(), `UPDATE community.import_grants SET remaining_bytes=0 WHERE id=$1`, grant["id"])
	result := unpack[struct {
		Items []kb.BatchOutcome `json:"items"`
	}](t, op(t, h, publisher, "submission.create", l.ID, map[string]any{"items": []kb.BatchItem{{Key: id(), Document: document()}}}, 200))
	if len(result.Items) != 1 || result.Items[0].Submission == nil {
		t.Fatalf("empty allowance blocked ordinary quota: %+v", result)
	}
	s.DB.Exec(context.Background(), `UPDATE community.import_grants SET remaining_bytes=100000 WHERE id=$1`, grant["id"])
	a, b := document(), document()
	a.SourcePath = "docs/new-a.md"
	b.SourcePath = "docs/new-b.md"
	result = unpack[struct {
		Items []kb.BatchOutcome `json:"items"`
	}](t, op(t, h, publisher, "submission.create", l.ID, map[string]any{"items": []kb.BatchItem{{Key: id(), Document: a}, {Key: id(), Document: b}}}, 200))
	for _, item := range result.Items {
		if item.Submission == nil {
			t.Fatalf("partial allowance blocked selection: %+v", result)
		}
	}
}

func TestReviewPacketAppliesResolutionInOneTransaction(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "review-packet", "private", "trusted")
	first := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	d := document()
	d.Markdown += "\nThe measured condition requires the engine clock to be running."
	update := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	packet := unpack[reviewPacket](t, op(t, h, owner, "submission.packet", l.ID, map[string]string{"id": update.ID}, 200))
	if !packet.Submission.IdentityReview || len(packet.Comparisons) == 0 {
		t.Fatal("missing identity comparison")
	}
	params := map[string]any{"id": update.ID, "digest": update.Digest, "resolution_version": packet.Submission.ResolutionVersion, "policy_version": l.PolicyVersion, "decision": "accept", "packet_token": packet.Token, "plan": resolutionPlan{Mode: "revision", FindingID: first.FindingID, BaseRevision: first.Revision}}
	accepted := unpack[kb.Submission](t, op(t, h, owner, "submission.review", l.ID, params, 200))
	if accepted.State != "accepted" || accepted.FindingID != first.FindingID || accepted.Revision == first.Revision {
		t.Fatal("atomic review did not apply correction")
	}
	op(t, h, owner, "submission.review", l.ID, params, 409)
}

func TestReviewExactCopyHonorsDisplayedDistinctAndContributionPlans(t *testing.T) {
	for _, mode := range []string{"create", "contribution"} {
		t.Run(mode, func(t *testing.T) {
			s, h := fixture(t)
			owner := id()
			l := create(t, h, owner, "copy-plan", "private", "trusted")
			first := submit(t, h, owner, l.ID, id(), document())
			runJob(t, s)
			first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
			d := document()
			d.SourcePath = "docs/independent-copy.md"
			copy := submit(t, h, owner, l.ID, id(), d)
			packet := unpack[reviewPacket](t, op(t, h, owner, "submission.packet", l.ID, map[string]string{"id": copy.ID}, 200))
			plan := resolutionPlan{Mode: mode}
			if mode == "contribution" {
				plan.FindingID, plan.BaseRevision = first.FindingID, first.Revision
			}
			accepted := unpack[kb.Submission](t, op(t, h, owner, "submission.review", l.ID, map[string]any{"id": copy.ID, "digest": copy.Digest, "resolution_version": packet.Submission.ResolutionVersion, "policy_version": l.PolicyVersion, "decision": "accept", "packet_token": packet.Token, "plan": plan}, 200))
			if accepted.State != "accepted" || accepted.FindingID == first.FindingID {
				t.Fatal("explicit publication effect was overridden by exact-copy routing")
			}
			rels := unpack[[]kb.Relation](t, op(t, h, owner, "finding.relations", l.ID, map[string]string{"id": accepted.FindingID}, 200))
			if (mode == "contribution") != (len(rels) == 1) {
				t.Fatalf("wrong contribution effect: %+v", rels)
			}
		})
	}
}

func TestReviewPacketHydratesPublicationTargetOutsideShortlist(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "packet-target", "private", "trusted")
	d := document()
	d.Markdown = "---\nkind: fact\nstatus: promoted\ngrade: direct\n---\n# Quartz\nQuartz ratio equals seven."
	first := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	first = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": first.ID}, 200))
	d.FindingID, d.BaseRevision = first.FindingID, first.Revision
	d.Markdown = "---\nkind: fact\nstatus: promoted\ngrade: direct\n---\n# Xenon\nPurple whistles."
	next := submit(t, h, owner, l.ID, id(), d)
	packet := unpack[reviewPacket](t, op(t, h, owner, "submission.packet", l.ID, map[string]string{"id": next.ID}, 200))
	found := false
	for _, c := range packet.Comparisons {
		if c.Reference.ID == first.FindingID && c.Reference.Version == first.Revision && c.Claim.Title == "Quartz" {
			found = true
		}
	}
	if !found {
		t.Fatal("publication target was not reviewable")
	}
}

func TestHealthRetainsFailureAcrossComparisonChunks(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "health-chunks", "private", "maintainer")
	l.Policy.Assistance = true
	op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
	ctx := context.Background()
	var first string
	for i := 0; i < 10; i++ {
		d := document()
		d.SourcePath = fmt.Sprintf("docs/clock-%d.md", i)
		d.Markdown += fmt.Sprintf("\nCondition %d also applies.", i)
		v := submit(t, h, owner, l.ID, id(), d)
		if i == 0 {
			first = v.ID
		}
	}
	if _, err := s.DB.Exec(ctx, `UPDATE community.health_work SET available_at=CASE WHEN subject_id::text=$2 THEN now() ELSE now()+interval '2 days' END WHERE community_id=$1`, l.ID, first); err != nil {
		t.Fatal(err)
	}
	s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	s.Semantic.Key = func() string { return "" }
	if ok, err := s.RunHealthJob(ctx); !ok || err != nil {
		t.Fatalf("first chunk: %v %v", ok, err)
	}
	var offset int
	var failure string
	if err := s.DB.QueryRow(ctx, `SELECT candidate_offset,scan_error FROM community.health_work WHERE subject_id::text=$1`, first).Scan(&offset, &failure); err != nil || offset != 8 || failure == "" {
		t.Fatalf("first chunk missing: %d %s %v", offset, failure, err)
	}
	s.Semantic = nil
	s.DB.Exec(ctx, `UPDATE community.health_work SET available_at=now() WHERE subject_id::text=$1`, first)
	if ok, err := s.RunHealthJob(ctx); !ok || err != nil {
		t.Fatalf("last chunk: %v %v", ok, err)
	}
	var analyzed int64
	if err := s.DB.QueryRow(ctx, `SELECT analyzed_generation,last_error FROM community.health_work WHERE subject_id::text=$1`, first).Scan(&analyzed, &failure); err != nil || analyzed == 0 || failure == "" {
		t.Fatal("successful later chunk hid earlier failure")
	}
}

func TestHealthCanonicalGroupsAndDirtyProgress(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "group-health", "private", "trusted")
	ctx := context.Background()
	claims := []kb.Submission{}
	for i := 0; i < 3; i++ {
		d := document()
		d.SourcePath = fmt.Sprintf("docs/variant-%d.md", i)
		d.Markdown += fmt.Sprintf("\nWording variant %d.", i)
		v := submit(t, h, owner, l.ID, id(), d)
		runJob(t, s)
		v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
		if v.State != "accepted" {
			v = approveIdentity(t, h, owner, l, v.ID, "", "")
		}
		claims = append(claims, v)
	}
	a, b, c := claims[0], claims[1], claims[2]
	issue := newHealthIssue(l.ID, l.PolicyVersion, claimRef{"finding", b.FindingID, b.Revision}, claimRef{"finding", c.FindingID, c.Revision}, "duplicate", "Compare wording", nil)
	if _, err := s.DB.Exec(ctx, `INSERT INTO community.health_issues(id,community_id,subject_kind,subject_id,subject_version,other_kind,other_id,other_version,kind,reason,policy_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, issue.ID, l.ID, issue.Subject.Kind, issue.Subject.ID, issue.Subject.Version, issue.Other.Kind, issue.Other.ID, issue.Other.Version, issue.Kind, issue.Reason, l.PolicyVersion); err != nil {
		t.Fatal(err)
	}
	s.DB.Exec(ctx, `UPDATE community.health_work SET analyzed_version=version,analyzed_policy=$2,analyzed_generation=generation WHERE community_id=$1`, l.ID, l.PolicyVersion)
	for _, v := range []kb.Submission{b, c} {
		op(t, h, owner, "finding.relate", l.ID, map[string]string{"source_id": v.FindingID, "source_revision": v.Revision, "target_id": a.FindingID, "target_revision": a.Revision, "kind": "equivalent", "rationale": "Same assertion and applicability."}, 200)
	}
	var state string
	s.DB.QueryRow(ctx, `SELECT state FROM community.health_issues WHERE id=$1`, issue.ID).Scan(&state)
	if state != "resolved" {
		t.Fatal("same canonical group retains duplicate issue")
	}
	progress := unpack[struct {
		Progress struct {
			Pending int `json:"pending"`
		} `json:"progress"`
	}](t, op(t, h, owner, "health.list", l.ID, map[string]any{}, 200))
	if progress.Progress.Pending != 3 {
		t.Fatalf("dirty work falsely counted checked: %+v", progress)
	}
	result := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "Wording variant 2", "limit": 1}, 200))
	if len(result.Hits) != 1 || result.Hits[0].FindingID != a.FindingID {
		t.Fatal("search did not hydrate canonical reader entry")
	}
}

func TestConcurrentContributorsShareClaimWithoutEvidence(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "concurrent-claims", "private", "trusted")
	users := []string{owner, id(), id()}
	for _, user := range users[1:] {
		invite := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "publisher"}, 200))
		op(t, h, user, "invite.redeem", "", map[string]any{"token": invite["token"]}, 200)
	}
	ids := []string{}
	for i, user := range users {
		d := document()
		d.SourcePath = "docs/claim-" + user + ".md"
		d.Evidence = nil
		if i == 1 {
			d.Evidence = []kb.Evidence{{Label: "Private research", Unavailable: true}}
		}
		v := submit(t, h, user, l.ID, id(), d)
		ids = append(ids, v.ID)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() { _, err := s.RunJob(context.Background()); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var findings, accepted int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.findings WHERE community_id=$1`, l.ID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.submissions WHERE community_id=$1 AND state='accepted'`, l.ID).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if findings != 1 || accepted != 3 {
		t.Fatalf("concurrent result: %d findings, %d accepted", findings, accepted)
	}
	one := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": ids[0]}, 200))
	credits := unpack[[]map[string]any](t, op(t, h, owner, "finding.contributions", l.ID, map[string]string{"id": one.FindingID}, 200))
	if len(credits) != 3 {
		t.Fatalf("lost attribution: %d", len(credits))
	}
	op(t, h, owner, "finding.assess", l.ID, map[string]string{"id": one.FindingID, "revision": one.Revision, "status": "disputed", "reason": "Clarify the supported build."}, 200)
}

func TestHealthFindsPendingDuplicatesWithoutLeakingThem(t *testing.T) {
	s, h := fixture(t)
	owner, a, b := id(), id(), id()
	l := create(t, h, owner, "pending-health", "private", "maintainer")
	for _, user := range []string{a, b} {
		invite := unpack[map[string]any](t, op(t, h, owner, "invite.create", l.ID, map[string]string{"role": "contributor"}, 200))
		op(t, h, user, "invite.redeem", "", map[string]any{"token": invite["token"]}, 200)
	}
	d := document()
	d.Evidence = nil
	one := submit(t, h, a, l.ID, id(), d)
	d.SourcePath = "docs/other-private-source.md"
	two := submit(t, h, b, l.ID, id(), d)
	worked, err := s.RunHealthJob(context.Background())
	if err != nil || !worked {
		t.Fatalf("health: %v %v", worked, err)
	}
	result := unpack[struct {
		Items []healthIssue `json:"items"`
	}](t, op(t, h, owner, "health.list", l.ID, map[string]any{}, 200))
	if len(result.Items) != 1 || result.Items[0].Kind != "duplicate" {
		t.Fatalf("pending duplicate absent: %+v", result)
	}
	op(t, h, a, "health.list", l.ID, map[string]any{}, 403)
	packet := op(t, h, a, "submission.packet", l.ID, map[string]string{"id": one.ID}, 200)
	if strings.Contains(string(packet), two.ID) || strings.Contains(string(packet), "other-private-source") {
		t.Fatal("another contributor's pending claim leaked")
	}
	op(t, h, a, "submission.packet", l.ID, map[string]string{"id": two.ID}, 404)
}

type healthTransport func(*http.Request) (*http.Response, error)

func (f healthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHealthLeaseCannotCompleteAfterConcurrentEdit(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "health-race", "private", "maintainer")
	l.Policy.Assistance = true
	op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
	one := document()
	one.Evidence = nil
	submit(t, h, owner, l.ID, id(), one)
	two := one
	two.SourcePath = "docs/other.md"
	two.Markdown += "\nThe elapsed time changes proportionally."
	submit(t, h, owner, l.ID, id(), two)
	s.Semantic = semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	s.Semantic.Key = func() string { return "test-key" }
	var called atomic.Bool
	s.Semantic.HTTP = &http.Client{Transport: healthTransport(func(r *http.Request) (*http.Response, error) {
		if !called.Swap(true) {
			if _, err := s.DB.Exec(context.Background(), `UPDATE community.submissions SET resolution_version=resolution_version+1 WHERE community_id=$1`, l.ID); err != nil {
				t.Error(err)
			}
		}
		body := `{"model":"` + semantic.Model + `","answers":{"scope":{"type":"choice","choice":"same","confidence":1,"probabilities":{"same":1,"overlap":0,"different":0,"unknown":0}},"relationship":{"type":"choice","choice":"equivalent","confidence":1,"probabilities":{"equivalent":1,"partial":0,"conflicting":0,"related":0,"unrelated":0,"unknown":0}}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	worked, err := s.RunHealthJob(context.Background())
	if err != nil || !worked || !called.Load() {
		t.Fatalf("comparison not exercised: %v %v", worked, err)
	}
	var stale, count int
	s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.health_work WHERE community_id=$1 AND analyzed_version<>''`, l.ID).Scan(&stale)
	s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.health_issues WHERE community_id=$1`, l.ID).Scan(&count)
	if stale != 0 || count != 0 {
		t.Fatal("stale lease marked new content complete or published a stale issue")
	}
}

func TestHealthInventoryIsNotLimitedToSubmissionList(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "whole-inventory", "private", "maintainer")
	ctx := context.Background()
	for range 105 {
		sid := id()
		d := document()
		d.SourcePath = "docs/" + sid + ".md"
		if _, err := s.DB.Exec(ctx, `INSERT INTO community.submissions(id,community_id,author_id,idempotency_key,digest,document,policy_version,claim_digest) VALUES($1,$2,$3,$4,$5,$6,1,$7)`, sid, l.ID, owner, id(), kb.Digest(d), raw(d), kb.ClaimDigest(d)); err != nil {
			t.Fatal(err)
		}
	}
	result := unpack[struct {
		Progress struct {
			Total   int `json:"total"`
			Pending int `json:"pending"`
		} `json:"progress"`
	}](t, op(t, h, owner, "health.list", l.ID, map[string]any{}, 200))
	if result.Progress.Total != 105 || result.Progress.Pending != 105 {
		t.Fatalf("inventory truncated: %+v", result)
	}
}
