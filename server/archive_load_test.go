package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	ka "github.com/adiazpar/re-discipline/retrieval/knowledgearchive"
)

func seedArchiveLoad(t *testing.T, s *Server, cid string, available int) int64 {
	t.Helper()
	mib := setting("LOAD_ARCHIVE_MIB", 256)
	rows := min(mib, available)
	size := mib * (1 << 20) / rows
	if size > 2<<20 {
		t.Fatal("increase document count for this archive size")
	}
	// Synthetic evidence is legitimate knowledge, but highly repetitive. This
	// measures bounded export memory and interference, not real-corpus ratios.
	paragraph := "Synthetic reverse-engineering evidence: the recorded instruction trace confirms register values, call order and simulation clock timing. This repeated fixture is not real game research.\n"
	excerpt := strings.Repeat(paragraph, size/len(paragraph)+1)[:size]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, e := s.DB.Exec(ctx, `UPDATE community.revisions SET assessment_evidence=jsonb_build_array(jsonb_build_object('label','Synthetic archive load evidence','excerpt',$2::text||id::text)) WHERE community_id=$1 AND id IN (SELECT id FROM community.revisions WHERE community_id=$1 ORDER BY id LIMIT $3)`, cid, excerpt, rows); e != nil {
		t.Fatal(e)
	}
	var bytes int64
	if e := s.DB.QueryRow(ctx, `SELECT sum(octet_length(assessment_evidence::text)) FROM community.revisions WHERE community_id=$1`, cid).Scan(&bytes); e != nil {
		t.Fatal(e)
	}
	return bytes
}

func startArchiveLoad(t *testing.T, s *Server, h *httptest.Server, operator, cid string) (string, func() map[string]any) {
	t.Helper()
	ticket := prepareDeletion(t, h, operator, cid, true)
	client := &kb.Client{URL: h.URL, Token: operator, HTTP: h.Client()}
	path := filepath.Join(t.TempDir(), "load.re-knowledge.jsonl.zst")
	type result struct {
		summary ka.Summary
		err     error
		seconds float64
	}
	finished := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	start := time.Now()
	go func() {
		_, e := client.DownloadArchive(ctx, cid, ticket, path)
		var sum ka.Summary
		if e == nil {
			sum, e = ka.Verify(path)
		}
		finished <- result{sum, e, time.Since(start).Seconds()}
	}()
	// Findings are small pointers. Start searches only once the export reaches
	// the large revision evidence, otherwise the comparison misses bulk I/O.
	var initialChunk, initialRecords int64
	var initialSection int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if e := s.DB.QueryRow(ctx, `SELECT next_chunk,section,records FROM community.deletion_sessions WHERE community_id=$1`, cid).Scan(&initialChunk, &initialSection, &initialRecords); e != nil {
			t.Fatal(e)
		}
		if initialSection == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if initialSection != 2 {
		t.Fatal("archive did not reach revision evidence before query measurement")
	}
	return ticket, func() map[string]any {
		var chunks, records int64
		var section int
		if e := s.DB.QueryRow(ctx, `SELECT next_chunk,section,records FROM community.deletion_sessions WHERE community_id=$1`, cid).Scan(&chunks, &section, &records); e != nil {
			t.Fatal(e)
		}
		if chunks <= initialChunk {
			t.Fatal("archive made no progress during search measurement")
		}
		r := <-finished
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.summary.CommunityID != cid {
			t.Fatal("archive community mismatch")
		}
		return map[string]any{"seconds": r.seconds, "bytes": r.summary.Bytes, "uncompressed_bytes": r.summary.UncompressedBytes, "records": r.summary.Records, "chunks_before_query_phase": initialChunk, "records_before_query_phase": initialRecords, "section_before_query_phase": initialSection, "chunks_completed_by_end_of_query_phase": chunks, "records_by_end_of_query_phase": records, "section_by_end_of_query_phase": section, "sha256": r.summary.SHA256, "knowledge_verified": true, "notes": fmt.Sprintf("Synthetic repetitive evidence; %.1f MiB uncompressed. Archive client, compression and queries share the limited app container; PostgreSQL runs separately.", float64(r.summary.UncompressedBytes)/(1<<20))}
	}
}
