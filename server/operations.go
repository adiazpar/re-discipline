package server

import (
	"context"
	"encoding/json"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

func (s *Server) dispatch(ctx context.Context, tx pgx.Tx, user string, req kb.Request) (any, error) {
	if req.Action == "community.create" {
		if user != s.Config.OperatorID {
			return nil, fail(403, "server operator required")
		}
		var l kb.Library
		if err := data(req.Data, &l); err != nil {
			return nil, err
		}
		if l.Visibility == "" {
			l.Visibility = "private"
		}
		if l.Visibility != "private" {
			return nil, fail(400, "new communities must be private")
		}
		if err := kb.ValidateLibrary(l); err != nil {
			return nil, fail(400, err.Error())
		}
		var total int
		var used int64
		if err := tx.QueryRow(ctx, `SELECT libraries,bytes FROM community.service_capacity()`).Scan(&total, &used); err != nil {
			return nil, err
		}
		if total >= setting("MAX_TOTAL_COMMUNITIES", 1000) {
			return nil, fail(429, "service community capacity reached")
		}
		l.ID = id()
		l.PolicyVersion = 1
		l.Role = "owner"
		if _, err := tx.Exec(ctx, `INSERT INTO community.libraries(id,owner_id,name,slug,visibility,policy) VALUES($1,$2,$3,$4,$5,$6)`, l.ID, user, l.Name, l.Slug, l.Visibility, raw(l.Policy)); err != nil {
			return nil, err
		}
		return l, audit(ctx, tx, l.ID, user, req.Action, map[string]any{"slug": l.Slug})
	}
	if req.Action == "community.list" {
		if user != s.Config.OperatorID {
			return nil, fail(403, "server operator required")
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM community.libraries WHERE owner_id=$1 AND deletion_id IS NULL ORDER BY id LIMIT 1001`, user)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for rows.Next() {
			var v string
			if err = rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
		out := []kb.Library{}
		for _, v := range ids {
			l, e := library(ctx, tx, v)
			if e != nil {
				return nil, e
			}
			out = append(out, l)
		}
		return out, nil
	}
	l, err := library(ctx, tx, req.Community)
	if err != nil {
		return nil, err
	}
	switch req.Action {
	case "community.get":
		return l, nil
	case "query":
		return s.query(ctx, tx, l, req.Data)
	case "knowledge.get":
		var p struct {
			ID string `json:"id"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if err = validUUID(p.ID); err != nil {
			return nil, err
		}
		var d kb.KnowledgeDocument
		err = tx.QueryRow(ctx, `SELECT id::text,source_path,markdown,fingerprint FROM community.documents WHERE community_id=$1 AND id=$2`, l.ID, p.ID).Scan(&d.ID, &d.Path, &d.Markdown, &d.Fingerprint)
		return d, err
	}
	if user != s.Config.OperatorID || l.Role != "owner" {
		return nil, fail(403, "server operator required")
	}
	switch req.Action {
	case "community.update":
		var p struct {
			Name            string    `json:"name"`
			Visibility      string    `json:"visibility"`
			Policy          kb.Policy `json:"policy"`
			ExpectedVersion int       `json:"expected_version"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		next := l
		next.Name = p.Name
		next.Visibility = p.Visibility
		next.Policy = p.Policy
		if err = kb.ValidateLibrary(next); err != nil {
			return nil, fail(400, err.Error())
		}
		tag, e := tx.Exec(ctx, `UPDATE community.libraries SET name=$1,visibility=$2,policy=$3,policy_version=policy_version+1 WHERE id=$4 AND policy_version=$5`, p.Name, p.Visibility, raw(p.Policy), l.ID, p.ExpectedVersion)
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, fail(409, "community settings changed; refresh before updating")
		}
		return library(ctx, tx, l.ID)
	case "knowledge.inventory":
		return s.knowledgeInventory(ctx, tx, l, req.Data)
	case "knowledge.put":
		return s.putKnowledge(ctx, tx, l, req.Data)
	case "knowledge.delete":
		return s.deleteKnowledge(ctx, tx, l, req.Data)
	case "usage":
		return s.usage(ctx, tx, l.ID)
	case "audit.list":
		rows, e := tx.Query(ctx, `SELECT action,detail FROM community.audit WHERE community_id=$1 ORDER BY id DESC LIMIT 100`, l.ID)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var action string
			var detail json.RawMessage
			if e = rows.Scan(&action, &detail); e != nil {
				return nil, e
			}
			out = append(out, map[string]any{"action": action, "detail": detail})
		}
		return out, rows.Err()
	}
	return nil, fail(400, "operation retired or unknown; use knowledge.upload through the plugin")
}
