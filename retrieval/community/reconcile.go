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
	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
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
	if err = indexdb.ReadRow(ctx, db, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='copy_bases'`).Scan(&exists); err != nil || exists == 0 {
		return out, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{copyService(conn.Service), conn.CommunityID, KnowledgeFormat}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := indexdb.ReadRows(ctx, db, `SELECT id,fingerprint,sequence FROM copy_bases WHERE service=? AND community=? AND format=? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
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
	db, err := indexdb.OpenWrite(filepath.Join(root, ".re-discipline", "index.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	return indexdb.Retry(ctx, func() error { return storeCopyBases(ctx, db, conn, entries) })
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

// A candidate's identity is reconciled once across all selected sources. Remote
// responses may arrive in any order; only a shared fingerprint establishes a
// change, never another community's sequence number or wall-clock timestamp.
func reconcileSources(ctx context.Context, root string, candidates, localHits []Result, connections []Connection, remotes []remoteResult, includeLocal bool) ([]Result, error) {
	ids := candidateIdentityIDs(candidates)
	locals := map[string]Result{}
	if includeLocal {
		copies, err := engine.LookupCopies(ctx, root, ids)
		if err != nil {
			return nil, err
		}
		for _, h := range copies {
			if (h.Status != "promoted" && h.Status != "superseded") || (h.Kind != "fact" && h.Kind != "reference") {
				continue
			}
			if _, ok := locals[h.FindingID]; ok {
				return nil, fmt.Errorf("duplicate curated finding identity %s; assign distinct IDs to separate findings", h.FindingID)
			}
			locals[h.FindingID] = Result{Source: "local", FindingID: h.FindingID, Fingerprint: h.Fingerprint, Path: h.Path, Kind: h.Kind, Status: h.Status, CreatedAt: h.CreatedAt, KnowledgeUpdatedAt: h.KnowledgeUpdatedAt, MetadataOnly: true}
		}
	}
	groups := map[string][]sourceCopy{}
	for id, h := range locals {
		groups[id] = append(groups[id], sourceCopy{hit: h})
	}
	for _, h := range localHits {
		if h.Source == "local" && validCopyIdentity(h) {
			if l, ok := locals[h.FindingID]; ok && l.Fingerprint == h.Fingerprint {
				groups[h.FindingID][0].hit = h
			}
		}
	}
	live := map[string]QueryResult{}
	pending := make([][]KnowledgeEntry, len(connections))
	for i, conn := range connections {
		if remotes[i].err != nil {
			continue
		}
		remote := remotes[i].result
		live[conn.Alias] = remote
		states := map[string]Result{}
		for _, e := range remote.Compared {
			h := Result{FindingID: e.ID, Fingerprint: e.Fingerprint, Sequence: e.Sequence}
			remoteOrigin(&h, conn)
			h.MetadataOnly = true
			states[e.ID] = h
		}
		for _, h := range remote.Hits {
			if validCopyIdentity(h) {
				states[h.FindingID] = h
			}
		}
		bases := map[string]copyBase{}
		if includeLocal {
			var err error
			bases, err = readCopyBases(ctx, root, conn, ids)
			if err != nil {
				return nil, err
			}
		}
		agreements := []KnowledgeEntry{}
		for _, id := range ids {
			h, ok := states[id]
			if !ok {
				continue
			}
			groups[id] = append(groups[id], sourceCopy{hit: h, base: bases[id]})
			if l, ok := locals[id]; ok && l.Fingerprint == h.Fingerprint && (bases[id].Fingerprint != h.Fingerprint || bases[id].Sequence < h.Sequence) {
				agreements = append(agreements, KnowledgeEntry{ID: id, Fingerprint: h.Fingerprint, Sequence: h.Sequence})
			}
		}
		pending[i] = agreements
	}
	// Only advance agreements when all observed copies agree. Updating one
	// equal mirror during a divergent query can erase contrary lineage and make
	// the next identical query choose a different winner.
	for i, agreements := range pending {
		settled := []KnowledgeEntry{}
		for _, e := range agreements {
			same := true
			for _, copy := range groups[e.ID] {
				if copy.hit.Fingerprint != e.Fingerprint {
					same = false
					break
				}
			}
			if same {
				settled = append(settled, e)
			}
		}
		if len(settled) > 0 {
			if err := rememberCopies(ctx, root, connections[i], settled); err != nil {
				return nil, err
			}
		}
	}
	out := []Result{}
	for _, candidate := range candidates {
		if validCopyIdentity(candidate) {
			copies := groups[candidate.FindingID]
			if len(copies) > 0 {
				if hit, ok := resolveGroup(candidate.FindingID, copies); ok {
					out = append(out, hit)
				}
				continue
			}
		}
		// Legacy/incomplete identities are independent results, never evidence of
		// equivalence. Failed sources cannot retain their first-round results.
		if candidate.Source == "local" {
			out = append(out, candidate)
			continue
		}
		for _, h := range live[candidate.Source].Hits {
			if h.FindingID == candidate.FindingID && h.Fingerprint == candidate.Fingerprint {
				out = append(out, h)
				break
			}
		}
	}
	return out, nil
}

type sourceCopy struct {
	hit  Result
	base copyBase
}

func resolveGroup(id string, copies []sourceCopy) (Result, bool) {
	// Stable presentation and read locations, independent of response/config order.
	sort.Slice(copies, func(i, j int) bool {
		a, b := copies[i].hit, copies[j].hit
		if (a.Source == "local") != (b.Source == "local") {
			return a.Source == "local"
		}
		return a.Service+"\x00"+a.CommunityID+"\x00"+a.Source < b.Service+"\x00"+b.CommunityID+"\x00"+b.Source
	})
	byHash := map[string][]Result{}
	var local *Result
	for _, c := range copies {
		byHash[c.hit.Fingerprint] = append(byHash[c.hit.Fingerprint], c.hit)
		if c.hit.Source == "local" {
			h := c.hit
			local = &h
		}
	}
	hashes := make([]string, 0, len(byHash))
	for hash := range byHash {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	winner, reason := "", ""
	if len(hashes) == 1 {
		winner = hashes[0]
	} else if local != nil {
		winner = local.Fingerprint
		remoteWinners, reasons := map[string]bool{}, map[string]bool{}
		for _, c := range copies {
			if c.hit.Source == "local" || c.hit.Fingerprint == local.Fingerprint {
				continue
			}
			choice, why := copyChoice(local.Fingerprint, c.hit.Fingerprint, c.hit.Sequence, c.base)
			reasons[why] = true
			if choice == "remote" {
				remoteWinners[c.hit.Fingerprint] = true
			}
		}
		switch {
		case reasons["local_preferred_no_baseline"]:
			reason = "local_preferred_no_baseline"
		case reasons["local_preferred_diverged"] || len(remoteWinners) > 1:
			reason = "local_preferred_diverged"
		case len(remoteWinners) == 1:
			for hash := range remoteWinners {
				winner = hash
			}
			reason = "remote_changed"
			// A second link may establish the opposite ordering: this proposed
			// remote winner was already a shared base that local changed away
			// from. Conflicting lineage cannot establish one global successor.
			for _, c := range copies {
				if c.base.Fingerprint == winner {
					winner, reason = local.Fingerprint, "local_preferred_diverged"
					break
				}
			}
		case reasons["remote_index_behind"]:
			reason = "remote_index_behind"
		default:
			reason = "local_changed"
		}
	}
	if winner != "" {
		hit := copyVariant(byHash[winner])
		if hit.MetadataOnly {
			return Result{}, false
		} // Obsolete keyword suppression.
		hit.Resolution = reason
		hit.ContentDiffers = len(hashes) > 1
		for _, c := range copies {
			if c.hit.Fingerprint != winner {
				hit.OtherCopies = append(hit.OtherCopies, copyLocation(c.hit))
			}
		}
		return hit, true
	}
	// No local authority or shared baseline can order independent remote edits.
	// One ranking slot contains explicit variants, including nonmatching metadata;
	// no arbitrary source's text is presented as the authoritative group snippet.
	group := Result{Source: "multiple", FindingID: id, Title: "Differing community copies", ContentDiffers: true, Resolution: "community_variants"}
	matched := false
	for _, hash := range hashes {
		variant := copyVariant(byHash[hash])
		matched = matched || !variant.MetadataOnly
		group.Variants = append(group.Variants, variant)
	}
	return group, matched
}

func copyLocation(h Result) Location {
	return Location{Source: h.Source, Service: h.Service, CommunityID: h.CommunityID, FindingID: h.FindingID, Fingerprint: h.Fingerprint, Path: h.Path, URL: h.URL}
}

func copyVariant(copies []Result) Result {
	hit := copies[0]
	for _, c := range copies {
		if !c.MetadataOnly {
			hit = c
			break
		}
	}
	hit.Locations = nil
	for _, c := range copies {
		hit.Locations = append(hit.Locations, copyLocation(c))
	}
	return hit
}
