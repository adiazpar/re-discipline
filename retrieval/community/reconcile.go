package community

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

type copyBase struct {
	Fingerprint string
	Sequence    int64
}

func copyService(service string) string {
	u, err := url.Parse(service)
	if err != nil {
		return service
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func readCopyBases(ctx context.Context, root string, conn Connection, ids []string) (map[string]copyBase, error) {
	out := map[string]copyBase{}
	db, err := engine.ReadRemoteIndex(root)
	if err != nil {
		return out, err
	}
	defer db.Close()
	var exists int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='copy_bases'`).Scan(&exists); err != nil || exists == 0 {
		return out, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{copyService(conn.Service), conn.CommunityID, KnowledgeFormat}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT id,fingerprint,sequence FROM copy_bases WHERE service=? AND community=? AND format=? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b copyBase
		if err = rows.Scan(&id, &b.Fingerprint, &b.Sequence); err != nil {
			return out, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// Call under the project index lock. A delayed response must never roll back an
// upload acknowledgement or a newer observation. Only one hash is retained.
func storeCopyBases(ctx context.Context, db *sql.DB, conn Connection, entries []KnowledgeEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, knowledge.CacheTables["copy_bases"]); err != nil {
		return err
	}

	// Read hashes in bounded indexed batches so an unchanged large upload does
	// not execute thousands of no-op writes or scan unrelated connections.
	for start := 0; start < len(entries); start += 256 {
		batch := entries[start:min(start+256, len(entries))]
		args := []any{copyService(conn.Service), conn.CommunityID, KnowledgeFormat}
		for _, e := range batch {
			if !validCopyIdentity(Result{FindingID: e.ID, Fingerprint: e.Fingerprint}) || e.Sequence < 0 {
				return fmt.Errorf("invalid matching-copy metadata")
			}
			args = append(args, e.ID)
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,fingerprint,sequence FROM copy_bases WHERE service=? AND community=? AND format=? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, args...)
		if e != nil {
			return e
		}
		current := map[string]copyBase{}
		for rows.Next() {
			var id string
			var b copyBase
			if e = rows.Scan(&id, &b.Fingerprint, &b.Sequence); e != nil {
				rows.Close()
				return e
			}
			current[id] = b
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, e := range batch {
			if old, ok := current[e.ID]; ok && old.Sequence >= e.Sequence {
				continue
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO copy_bases(service,community,id,fingerprint,sequence,format) VALUES(?,?,?,?,?,?) ON CONFLICT(service,community,id) DO UPDATE SET fingerprint=excluded.fingerprint,sequence=excluded.sequence,format=excluded.format WHERE copy_bases.format<>excluded.format OR excluded.sequence>copy_bases.sequence`, copyService(conn.Service), conn.CommunityID, e.ID, e.Fingerprint, e.Sequence, KnowledgeFormat)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func rememberCopies(ctx context.Context, root string, conn Connection, entries []KnowledgeEntry) error {
	if len(entries) == 0 {
		return nil
	}
	release, ok := engine.TryLock(root)
	if !ok {
		return nil
	}
	defer release()
	db, err := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	return storeCopyBases(ctx, db, conn, entries)
}

func copyChoice(local, remote string, sequence int64, base copyBase) (string, string) {
	if local == remote {
		return "equal", ""
	}
	if base.Fingerprint != "" && sequence < base.Sequence {
		return "local", "remote_index_behind"
	}
	if base.Fingerprint == "" {
		return "local", "local_preferred_no_baseline"
	}
	if local == base.Fingerprint {
		return "remote", "remote_changed"
	}
	if remote == base.Fingerprint {
		return "local", "local_changed"
	}
	return "local", "local_preferred_diverged"
}

// Compare metadata for all candidate identities, including copies which no
// longer match the search words. Losing text is removed before blending/ranking;
// a nonmatching winner does not turn an obsolete match into a search result.
func reconcileCopies(ctx context.Context, root string, conn Connection, local []Result, remote QueryResult) ([]Result, error) {
	idsSet := map[string]bool{}
	for _, h := range append(append([]Result{}, local...), remote.Hits...) {
		if validCopyIdentity(h) {
			idsSet[h.FindingID] = true
		}
	}
	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	copies, err := engine.LookupCopies(ctx, root, ids)
	if err != nil {
		return nil, err
	}
	locals := map[string]engine.Hit{}
	for _, h := range copies {
		if (h.Status != "promoted" && h.Status != "superseded") || (h.Kind != "fact" && h.Kind != "reference") {
			continue
		}
		if _, ok := locals[h.FindingID]; ok {
			return nil, fmt.Errorf("duplicate curated finding identity %s; assign distinct IDs to separate findings", h.FindingID)
		}
		locals[h.FindingID] = h
	}
	states := map[string]KnowledgeEntry{}
	for _, h := range remote.Hits {
		if validCopyIdentity(h) {
			states[h.FindingID] = KnowledgeEntry{ID: h.FindingID, Fingerprint: h.Fingerprint, Sequence: h.Sequence}
		}
	}
	for _, e := range remote.Compared {
		states[e.ID] = e
	}
	bases, err := readCopyBases(ctx, root, conn, ids)
	if err != nil {
		return nil, err
	}
	choices := map[string]string{}
	reasons := map[string]string{}
	agreements := []KnowledgeEntry{}
	for id, r := range states {
		l, ok := locals[id]
		if !ok {
			choices[id] = "remote"
			continue
		}
		choice, reason := copyChoice(l.Fingerprint, r.Fingerprint, r.Sequence, bases[id])
		choices[id], reasons[id] = choice, reason
		if choice == "equal" && (bases[id].Fingerprint != r.Fingerprint || bases[id].Sequence < r.Sequence) {
			agreements = append(agreements, r)
		}
	}
	out := make([]Result, 0, len(local)+len(remote.Hits))
	for _, h := range append(append([]Result{}, local...), remote.Hits...) {
		choice := choices[h.FindingID]
		if h.Source == "local" {
			if choice == "remote" {
				continue
			}
			// The local index could have refreshed while the HTTP request was in flight.
			if l, ok := locals[h.FindingID]; ok && l.Fingerprint != h.Fingerprint {
				continue
			}
		} else if choice == "local" {
			continue
		}
		if reason := reasons[h.FindingID]; reason != "" {
			h.ContentDiffers = true
			h.Resolution = reason
			if h.Source == "local" {
				r := states[h.FindingID]
				h.OtherCopies = []Location{{Source: conn.Alias, Service: conn.Service, CommunityID: conn.CommunityID, FindingID: r.ID, Fingerprint: r.Fingerprint}}
			} else {
				l := locals[h.FindingID]
				h.OtherCopies = []Location{{Source: "local", FindingID: l.FindingID, Fingerprint: l.Fingerprint, Path: l.Path}}
			}
		}
		out = append(out, h)
	}
	if err = rememberCopies(ctx, root, conn, agreements); err != nil {
		return nil, err
	}
	return out, nil
}
