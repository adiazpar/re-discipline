package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	ka "github.com/adiazpar/re-discipline/retrieval/knowledgearchive"
	"github.com/klauspost/compress/zstd"
)

type archivePosition struct {
	Section int
	Key     string
	Chain   string
	Records int64
}
type archiveRequest struct {
	Ticket string `json:"ticket"`
	Index  int64  `json:"index"`
}

func (s *Server) archiveChunk(w http.ResponseWriter, r *http.Request, owner string, req kb.Request) {
	if e := exactCommunity(req.Community); e != nil {
		writeError(w, e)
		return
	}
	var p archiveRequest
	if json.Unmarshal(req.Data, &p) != nil || len(p.Ticket) < 32 || p.Index < 0 {
		writeError(w, fail(400, "valid archive ticket and chunk index required"))
		return
	}
	// One bounded encoder/query at a time; never occupy a search scheduler slot.
	select {
	case s.archiveSlot <- struct{}{}:
		defer func() { <-s.archiveSlot }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, fail(503, "archive worker busy"))
		return
	}
	if wait := time.Until(s.archiveNext); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	stats := s.scheduler.stats()
	if stats["queued"].(int) > max(s.budget.Workers*4, s.budget.Queue/4) {
		w.Header().Set("Retry-After", "1")
		writeError(w, fail(503, "archive yielding to retrieval traffic"))
		return
	}
	started := time.Now()
	body, next, chain, records, done, e := s.buildArchiveChunk(r.Context(), owner, req.Community, p)
	if e != nil {
		writeError(w, e)
		return
	}
	encoder, e := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(128<<10), zstd.WithLowerEncoderMem(true), zstd.WithEncoderCRC(true))
	if e != nil {
		writeError(w, e)
		return
	}
	compressed := encoder.EncodeAll(body, nil)
	encoder.Close()
	// Budget raw bytes too: highly compressible content still costs DB I/O and CPU.
	rate := max(1<<20, min(8<<20, int(s.budget.Memory/64)))
	delay := time.Duration(int64(len(body)) * int64(time.Second) / int64(rate))
	if stats["running"].(int) > 0 {
		delay = max(delay, time.Since(started)*3)
	}
	s.archiveNext = time.Now().Add(delay)

	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
	w.Header().Set("X-Archive-Next", strconv.FormatInt(next, 10))
	w.Header().Set("X-Archive-Chain", chain)
	w.Header().Set("X-Archive-Records", strconv.FormatInt(records, 10))
	w.Header().Set("X-Archive-Done", strconv.FormatBool(done))
	w.Header().Set("X-Archive-SHA256", ka.Digest(compressed))
	w.WriteHeader(200)
	w.Write(compressed)
}

// Committing before sending makes lost responses safe: the preceding cursor is
// retained and regenerated from frozen rows. No archive file is stored remotely.
func (s *Server) buildArchiveChunk(ctx context.Context, owner, cid string, p archiveRequest) (body []byte, next int64, chain string, records int64, done bool, err error) {
	tx, err := s.begin(ctx, owner)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='3s'`); err != nil {
		return
	}
	var frozen, actualOwner, name, scope string
	var sequence int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(deletion_id::text,''),owner_id::text,name,COALESCE(policy->>'scope',''),sequence FROM community.libraries WHERE id=$1 FOR SHARE`, cid).Scan(&frozen, &actualOwner, &name, &scope, &sequence)
	if err != nil {
		return
	}
	if owner != actualOwner {
		err = fail(403, "server operator ownership required")
		return
	}
	var sid, hash, phase string
	var expires time.Time
	var archive bool
	var index int64
	var at, previous archivePosition
	err = tx.QueryRow(ctx, `SELECT id::text,ticket_hash,phase,archive,expires_at,section,after_key,next_chunk,chain,records,previous_section,previous_key,previous_chain,previous_records FROM community.deletion_sessions WHERE community_id=$1 FOR UPDATE`, cid).Scan(&sid, &hash, &phase, &archive, &expires, &at.Section, &at.Key, &index, &at.Chain, &at.Records, &previous.Section, &previous.Key, &previous.Chain, &previous.Records)
	if err != nil {
		return
	}
	if frozen != sid || hash != ticketHash(p.Ticket) || !archive || time.Now().After(expires) || phase == "preview" {
		err = fail(409, "archive session expired, cancelled, or not locked")
		return
	}
	replay := index > 0 && p.Index == index-1
	if p.Index != index && !replay {
		err = fail(409, "archive cursor does not match; resume the last saved chunk")
		return
	}
	if !replay && phase != "locked" {
		err = fail(409, "archive is complete")
		return
	}
	if replay {
		at = previous
	}
	start := at
	var buf bytes.Buffer
	appendRecord := func(kind string, data any) {
		b := raw(map[string]any{"type": kind, "data": data})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if at.Section == 0 {
		appendRecord("manifest", map[string]any{"format": ka.Format, "community_id": cid, "name": name, "scope": scope, "sequence": sequence})
		at.Section = 1
	}
	chunkRecords := 0
	for at.Section <= 1 && buf.Len() < 2<<20 && chunkRecords < 64 {
		kind, query, args, e := archiveQuery(at.Section, at.Key, cid)
		if e != nil {
			err = e
			return
		}
		rows, e := tx.Query(ctx, query, args...)
		if e != nil {
			err = e
			return
		}
		count := 0
		full := false
		for rows.Next() {
			var key string
			var data []byte
			if e = rows.Scan(&key, &data); e != nil {
				break
			}
			if len(data) > ka.MaxRecord-1024 {
				e = fail(413, "knowledge record exceeds archive safety limit")
				break
			}
			appendRecord(kind, json.RawMessage(data))
			at.Key = key
			at.Records++
			count++
			chunkRecords++
			if buf.Len() >= 2<<20 {
				full = true
				break
			}
		}
		rows.Close()
		if e == nil {
			e = rows.Err()
		}
		if e != nil {
			err = e
			return
		}
		if full {
			break
		}
		if count == 0 {
			at.Section++
			at.Key = ""
		}
	}
	done = at.Section > 1
	if done {
		appendRecord("end", map[string]any{"records": at.Records, "chain": at.Chain})
	}
	body = buf.Bytes()
	if len(body) > ka.MaxChunk {
		err = fail(413, "archive chunk too large")
		return
	}
	chain = ka.Chain(start.Chain, body)
	next = p.Index + 1
	records = at.Records
	if replay {
		var expected string
		err = tx.QueryRow(ctx, `SELECT chain FROM community.deletion_sessions WHERE id=$1`, sid).Scan(&expected)
		if err == nil && expected != chain {
			err = fail(409, "archive snapshot changed; cancel and prepare again")
		}
		return
	}
	phase = "locked"
	if done {
		phase = "downloaded"
	}
	_, err = tx.Exec(ctx, `UPDATE community.deletion_sessions SET section=$2,after_key=$3,next_chunk=$4,chain=$5,records=$6,previous_section=$7,previous_key=$8,previous_chain=$9,previous_records=$10,phase=$11,expires_at=now()+interval '1 hour' WHERE id=$1`, sid, at.Section, at.Key, next, chain, records, start.Section, start.Key, start.Chain, start.Records, phase)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return
}

func archiveQuery(section int, after, cid string) (string, string, []any, error) {
	if section != 1 {
		return "", "", nil, fail(400, "invalid archive section")
	}
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	return "document", `SELECT id::text,jsonb_build_object('id',id,'path',source_path,'markdown',markdown,'fingerprint',fingerprint) FROM community.documents WHERE community_id=$1 AND id>$2::uuid ORDER BY id LIMIT 1`, []any{cid, after}, nil
}
