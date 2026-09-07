package community

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

type Command struct {
	Action    string          `json:"action"`
	Service   string          `json:"service,omitempty"`
	Community string          `json:"community,omitempty"`
	Alias     string          `json:"alias,omitempty"`
	Path      string          `json:"path,omitempty"`
	Build     string          `json:"build,omitempty"`
	DraftID   string          `json:"draft_id,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func Execute(ctx context.Context, root string, p Command) (any, error) {
	switch p.Action {
	case "connections":
		return LoadSettings(root)
	case "mode.set":
		s, e := LoadSettings(root)
		if e != nil {
			return nil, e
		}
		s.Mode = p.Mode
		return s, SaveSettings(root, s)
	case "publish.preview":
		return ReadDraft(root, p.DraftID)
	case "publish.revise":
		return Revise(ctx, root, p.DraftID)
	case "publish.queue":
		return Queue(root, p.DraftID)
	case "publish.flush":
		return Flush(ctx, root)
	case "publish.batch.flush":
		var v struct {
			ImportGrant string `json:"import_grant"`
		}
		if len(p.Data) > 0 {
			if e := json.Unmarshal(p.Data, &v); e != nil {
				return nil, e
			}
		}
		if v.ImportGrant != "" && p.Alias == "" {
			return nil, fmt.Errorf("an import allowance requires one explicit alias")
		}
		return FlushBatch(ctx, root, p.Alias, v.ImportGrant)
	case "publish.batch.queue", "publish.batch.export":
		var v struct {
			DraftIDs []string `json:"draft_ids"`
		}
		if e := json.Unmarshal(p.Data, &v); e != nil {
			return nil, e
		}
		if len(v.DraftIDs) == 0 || len(v.DraftIDs) > 20000 {
			return nil, fmt.Errorf("select 1 to 20000 draft IDs")
		}
		items := []BatchItem{}
		issues := []map[string]string{}
		destination := ""
		for _, id := range v.DraftIDs {
			d, e := ReadDraft(root, id)
			if e == nil && p.Alias != "" && d.Connection.Alias != p.Alias {
				e = fmt.Errorf("draft belongs to another destination")
			}
			if e == nil && p.Action == "publish.batch.queue" {
				d, e = Queue(root, id)
			}
			if e != nil {
				issues = append(issues, map[string]string{"draft_id": id, "error": e.Error()})
				continue
			}
			key := d.Connection.Service + "/" + d.Connection.CommunityID
			if destination != "" && destination != key {
				return nil, fmt.Errorf("export one community at a time")
			}
			destination = key
			if p.Action == "publish.batch.export" {
				if d.State != "queued" {
					issues = append(issues, map[string]string{"draft_id": id, "error": "preview and queue this draft before export"})
					continue
				}
				items = append(items, BatchItem{Key: d.ID, Document: d.Document})
			}
		}
		return map[string]any{"items": items, "issues": issues, "destination": destination}, nil
	case "publish.evidence":
		var v struct {
			Path  string `json:"path"`
			Label string `json:"label"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		}
		if e := json.Unmarshal(p.Data, &v); e != nil {
			return nil, e
		}
		return AttachEvidence(root, p.DraftID, v.Path, v.Label, v.Start, v.End)
	case "publish.list":
		paths, e := filepath.Glob(filepath.Join(root, ".re-discipline", "community", "drafts", "*.json"))
		if e != nil {
			return nil, e
		}
		out := []Draft{}
		for _, path := range paths {
			d, e := ReadDraft(root, strings.TrimSuffix(filepath.Base(path), ".json"))
			if e != nil {
				return nil, e
			}
			out = append(out, d)
		}
		return out, nil
	case "publish.update":
		d, e := ReadDraft(root, p.DraftID)
		if e != nil {
			return nil, e
		}
		if d.State != "draft" {
			return nil, fmt.Errorf("only an unqueued draft can be edited; prepare a new draft for a new submission")
		}
		var doc Document
		if e = json.Unmarshal(p.Data, &doc); e != nil {
			return nil, e
		}
		d.Document = doc
		return map[string]any{"draft": d, "checks": Validate(doc)}, atomicJSON(draftPath(root, d.ID), d)
	}
	settings, err := LoadSettings(root)
	if err != nil {
		return nil, err
	}
	var conn Connection
	if p.Alias != "" {
		for _, v := range settings.Connections {
			if v.Alias == p.Alias {
				conn = v
				break
			}
		}
		if conn.Alias == "" && p.Action != "connect" {
			return nil, fmt.Errorf("unknown community alias %q", p.Alias)
		}
	}
	if conn.Service != "" {
		p.Service = conn.Service
		p.Community = conn.CommunityID
	}
	if p.Action == "disconnect" {
		next := settings.Connections[:0]
		for _, v := range settings.Connections {
			if v.Alias != p.Alias {
				next = append(next, v)
			}
		}
		settings.Connections = next
		if len(next) == 0 {
			settings.Mode = "local"
		}
		if err = SaveSettings(root, settings); err != nil {
			return nil, err
		}
		return map[string]any{"disconnected": p.Alias, "cache_retained": true}, nil
	}
	if p.Service == "" {
		return nil, fmt.Errorf("provide a service URL or connected alias")
	}
	client, err := NewClient(p.Service)
	if err != nil {
		return nil, err
	}
	switch p.Action {
	case "publish.batch.prepare":
		if conn.Alias == "" {
			return nil, fmt.Errorf("publication requires a connected alias")
		}
		var v struct {
			Paths []string `json:"paths"`
		}
		if e := json.Unmarshal(p.Data, &v); e != nil {
			return nil, e
		}
		return PrepareBatch(root, conn, v.Paths, p.Build)
	case "publish.reconcile":
		if conn.Alias == "" {
			return nil, fmt.Errorf("reconciliation requires a connected alias")
		}
		var v struct {
			Sources []Adoption `json:"sources"`
		}
		if len(p.Data) > 0 {
			if e := json.Unmarshal(p.Data, &v); e != nil {
				return nil, e
			}
		}
		if _, e := client.Sync(ctx, root, conn); e != nil {
			return nil, e
		}
		return Reconcile(root, conn, v.Sources)
	case "login.start":
		return client.LoginStart(ctx)
	case "login.finish":
		return client.LoginFinish(ctx)
	case "logout":
		err = keyring.Delete("re-discipline-community", client.URL)
		return map[string]bool{"signed_out": true}, err
	case "connect":
		c, e := client.Connect(ctx, root, p.Alias, p.Community)
		if e != nil {
			return nil, e
		}
		_, e = client.Sync(ctx, root, c)
		if e != nil {
			return map[string]any{"connection": c, "sync_error": e.Error()}, nil
		}
		return c, nil
	case "sync":
		if conn.Alias == "" {
			return nil, fmt.Errorf("sync requires a connected alias")
		}
		return client.Sync(ctx, root, conn)
	case "dashboard":
		u := client.URL
		if p.Community != "" {
			u += "/communities/" + p.Community
		}
		return map[string]string{"url": u}, nil
	case "publish.prepare":
		if conn.Alias == "" {
			return nil, fmt.Errorf("publication requires a connected alias")
		}
		d, checks, e := Prepare(root, conn, p.Path, p.Build)
		if e != nil {
			return nil, e
		}
		return map[string]any{"draft": d, "checks": checks, "draft_path": draftPath(root, d.ID), "uploaded": false}, nil
	default:
		var out any
		var payload any = map[string]any{}
		if len(p.Data) > 0 {
			if err = json.Unmarshal(p.Data, &payload); err != nil {
				return nil, err
			}
		}
		err = client.Operation(ctx, p.Action, p.Community, payload, &out)
		return out, err
	}
}

func RunJSON(ctx context.Context, root string, b []byte) (string, error) {
	var p Command
	if err := json.Unmarshal(b, &p); err != nil {
		return "", err
	}
	out, err := Execute(ctx, root, p)
	if err != nil {
		return "", err
	}
	v, err := json.MarshalIndent(out, "", "  ")
	return string(v), err
}

func CommandSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action":  map[string]any{"type": "string", "description": "connections, connect, disconnect, login.start, login.finish, logout, mode.set, sync, dashboard, publish.prepare/preview/update/revise/queue/flush/list/evidence/reconcile, publish.batch.prepare/queue/flush/export, community.create/list/get/update, member.list/set/remove, invite.create/list/revoke/redeem, submission.create/batch/list/get/review, import.create/list/revoke, finding.get/history/withdraw/candidates/relations/relate, query, changes, export, usage, audit.list, token.list/revoke"},
		"service": map[string]any{"type": "string", "description": "HTTPS service origin; credentials remain in the OS credential store"}, "community": map[string]any{"type": "string", "description": "Community UUID or slug"}, "alias": map[string]any{"type": "string", "description": "Connected project alias"}, "path": map[string]any{"type": "string", "description": "Explicit docs/ Markdown finding for local publication preparation"}, "build": map[string]any{"type": "string"}, "draft_id": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"local", "external", "both"}}, "data": map[string]any{"type": "object", "description": "Operation-specific payload; use the community skill reference for schemas"}}, "required": []string{"action"}}
}

func ReadCommandFile(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return os.ReadFile("/dev/stdin")
	}
	return os.ReadFile(path)
}
