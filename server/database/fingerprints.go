package database

import (
	"context"
	"encoding/json"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

// Backfill in the migration transaction, so no deployment sees partial identity
// recovery. Existing IDs, authors and revisions are never rewritten.
func fingerprints(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id::text,document FROM community.revisions WHERE NOT deleted AND (content_digest='' OR claim_digest='')`)
	if err != nil {
		return err
	}
	type entry struct {
		id  string
		doc kb.Document
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		var b []byte
		if err = rows.Scan(&e.id, &b); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(b, &e.doc); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err = tx.Exec(ctx, `UPDATE community.revisions SET content_digest=$1,text_digest=$2,claim_digest=$4 WHERE id=$3`, kb.ContentDigest(e.doc), kb.TextDigest(e.doc.Markdown), e.id, kb.ClaimDigest(e.doc)); err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, `SELECT id::text,document FROM community.submissions WHERE claim_digest='' AND COALESCE(document->>'markdown','')<>''`)
	if err != nil {
		return err
	}
	entries = nil
	for rows.Next() {
		var e entry
		var b []byte
		if err = rows.Scan(&e.id, &b); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(b, &e.doc); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err = tx.Exec(ctx, `UPDATE community.submissions SET claim_digest=$1 WHERE id=$2`, kb.ClaimDigest(e.doc), e.id); err != nil {
			return err
		}
	}
	return nil
}
