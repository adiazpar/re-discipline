package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

// Bound memory independently of corpus size. Current UUIDs survive migration;
// unpublished drafts and historical content do not become current knowledge.
func migrateKnowledge(ctx context.Context, tx pgx.Tx) error {
	afterCommunity, afterID := "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := tx.Query(ctx, `SELECT f.community_id::text,f.id::text,r.document,r.sequence FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE NOT f.deleted AND NOT r.deleted AND r.status NOT IN ('refuted','superseded') AND (f.community_id,f.id)>($1::uuid,$2::uuid) ORDER BY f.community_id,f.id LIMIT 32`, afterCommunity, afterID)
		if err != nil {
			return err
		}
		type record struct {
			cid, id  string
			data     []byte
			sequence int64
		}
		batch := []record{}
		for rows.Next() {
			var r record
			if err = rows.Scan(&r.cid, &r.id, &r.data, &r.sequence); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, r := range batch {
			var legacy kb.Document
			if err = json.Unmarshal(r.data, &legacy); err != nil {
				return err
			}
			text := kb.ProjectKnowledge(legacy.Markdown).Markdown
			if !strings.HasPrefix(text, "---\n") {
				text = "---\nstatus: promoted\nkind: " + legacy.Kind + "\ngrade: " + legacy.Grade + "\nbuild: " + legacy.Build + "\n---\n" + text
			}
			// The accepted remote UUID is authoritative for migrated documents.
			lines := strings.Split(text, "\n")
			for i := 1; i < len(lines) && lines[i] != "---"; i++ {
				if strings.HasPrefix(lines[i], "id:") {
					lines[i] = "id: " + r.id
				}
			}
			text = strings.Join(lines, "\n")
			text, err = kb.WithFindingID(text, r.id)
			if err != nil {
				return fmt.Errorf("migrate finding %s: %w", r.id, err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO community.documents(community_id,id,source_path,markdown,fingerprint,generation) VALUES($1,$2,$3,$4,$5,$6)`, r.cid, r.id, legacy.SourcePath, text, kb.KnowledgeFingerprint(text), r.sequence)
			if err != nil {
				return err
			}
			afterCommunity, afterID = r.cid, r.id
		}
	}
}

func rehashKnowledge(ctx context.Context, tx pgx.Tx) error {
	afterCommunity, afterID := "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := tx.Query(ctx, `SELECT community_id::text,id::text,markdown,fingerprint FROM community.documents WHERE (community_id,id)>($1::uuid,$2::uuid) ORDER BY community_id,id LIMIT 32`, afterCommunity, afterID)
		if err != nil {
			return err
		}
		type item struct{ community, id, text, fingerprint string }
		var batch []item
		for rows.Next() {
			var r item
			if err := rows.Scan(&r.community, &r.id, &r.text, &r.fingerprint); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, r := range batch {
			if fp := kb.KnowledgeFingerprint(r.text); fp != r.fingerprint {
				if _, err := tx.Exec(ctx, `UPDATE community.documents SET fingerprint=$3 WHERE community_id=$1 AND id=$2`, r.community, r.id, fp); err != nil {
					return err
				}
			}
			afterCommunity, afterID = r.community, r.id
		}
	}
}
