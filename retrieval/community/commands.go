package community

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

type Command struct {
	Root      string          `json:"root,omitempty"`
	Retrieval string          `json:"retrieval,omitempty"`
	Action    string          `json:"action"`
	Service   string          `json:"service,omitempty"`
	Community string          `json:"community,omitempty"`
	Alias     string          `json:"alias,omitempty"`
	Path      string          `json:"path,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func Execute(ctx context.Context, root string, p Command) (any, error) {
	// A server health check needs neither a local project nor a saved credential.
	if p.Action == "server.ping" && p.Service != "" {
		return Ping(ctx, p.Service)
	}
	if p.Root != "" {
		var err error
		root, err = ProjectRoot(p.Root)
		if err != nil {
			return nil, err
		}
	}
	if err := validateRetrieval(p.Retrieval); err != nil {
		return nil, err
	}
	switch p.Action {
	case "status", "assistance.status":
		return AssistanceInfo(root)
	case "assistance.set":
		var config semantic.Config
		if err := json.Unmarshal(p.Data, &config); err != nil {
			return nil, err
		}
		config, err := config.Normalized()
		if err != nil {
			return nil, err
		}
		if err = atomicJSON(semantic.ConfigPath(root), config); err != nil {
			return nil, err
		}
		return AssistanceInfo(root)
	case "connections":
		return LoadSettings(root)
	case "retrieval.set":
		s, e := LoadSettings(root)
		if e != nil {
			return nil, e
		}
		if p.Retrieval == "" {
			return nil, fmt.Errorf("community retrieval is remote-only")
		}
		s.Retrieval = p.Retrieval
		return s, SaveSettings(root, s)
	case "mode.set":
		s, e := LoadSettings(root)
		if e != nil {
			return nil, e
		}
		s.Mode = p.Mode
		return s, SaveSettings(root, s)
	case "knowledge.identify":
		var options UploadOptions
		if err := json.Unmarshal(p.Data, &options); err != nil {
			return nil, err
		}
		return IdentifyKnowledge(root, options.Paths)
	}

	settings, err := LoadSettings(root)
	if err != nil {
		return nil, err
	}
	var conn Connection
	if p.Alias == "" && (readOperation(p.Action) || p.Action == "knowledge.upload" || p.Action == "knowledge.status") {
		if len(settings.Connections) > 1 {
			return nil, fmt.Errorf("disconnect extra legacy links before retrieval")
		}
		if len(settings.Connections) == 1 {
			conn = settings.Connections[0]
		}
	}
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
		if (p.Service != "" && strings.TrimRight(p.Service, "/") != conn.Service) || (p.Community != "" && p.Community != conn.CommunityID) {
			return nil, fmt.Errorf("explicit destination conflicts with the connected alias; use one destination")
		}
		p.Service = conn.Service
		p.Community = conn.CommunityID
	}
	if p.Action == "disconnect" {
		return Disconnect(ctx, root, p.Alias)
	}
	if p.Service == "" {
		return nil, fmt.Errorf("provide a service URL or connected alias")
	}
	if p.Action == "server.ping" {
		return Ping(ctx, p.Service)
	}
	client, err := NewClient(p.Service)
	if err == nil && conn.Service != "" && p.Action != "connect" && p.Action != "community.delete" && p.Action != "community.archive" && (readOperation(p.Action) || client.Token == "") {
		client, err = NewConnectionClient(conn, root)
	}
	if err != nil {
		return nil, err
	}
	switch p.Action {
	case "community.archive":
		var v struct {
			Ticket string `json:"ticket"`
		}
		if err = json.Unmarshal(p.Data, &v); err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, archiveSliceKey{}, time.Now().Add(15*time.Second))
		return client.DownloadArchive(ctx, p.Community, v.Ticket, p.Path)
	case "community.delete":
		if client.Token == "" {
			return nil, fmt.Errorf("community.delete requires server operator credentials")
		}
		var out map[string]any
		if err = client.Operation(ctx, p.Action, p.Community, json.RawMessage(p.Data), &out); err != nil {
			return nil, err
		}
		if out["community_id"] != p.Community {
			return nil, fmt.Errorf("server did not confirm deletion of the selected community")
		}
		if out["deleted"] != true {
			return out, nil
		}
		for _, linked := range settings.Connections {
			if linked.Service == client.URL && linked.CommunityID == p.Community {
				_, e := disconnect(ctx, root, linked.Alias, &linked)
				out["local_disconnected"] = e == nil
				if e != nil {
					out["notice"] = "Community deleted; local connection cleanup needs attention: " + e.Error()
				}
			}
		}
		return out, nil
	case "knowledge.upload", "knowledge.status":
		if conn.Alias == "" {
			return nil, fmt.Errorf("upload requires the project's connected community")
		}
		var options UploadOptions
		if len(p.Data) > 0 {
			if err := json.Unmarshal(p.Data, &options); err != nil {
				return nil, err
			}
		}
		report, err := UploadKnowledge(ctx, root, conn, options, p.Action == "knowledge.upload")
		if err != nil {
			report.issue(err.Error())
			return report, nil
		}
		return report, nil

	case "connect":
		var credentials struct {
			Invitation string `json:"invitation"`
		}
		if len(p.Data) > 0 {
			if e := json.Unmarshal(p.Data, &credentials); e != nil {
				return nil, e
			}
		}
		client.Invitation = credentials.Invitation
		return client.Connect(ctx, root, p.Alias, p.Community)
	default:
		switch p.Action {
		case "community.create", "community.list", "community.get", "community.update", "server.stats", "access.create", "access.list", "access.revoke", "knowledge.get", "knowledge.delete", "query", "usage", "audit.list":
		default:
			return nil, fmt.Errorf("unknown or retired operation; readers search and read; only the server operator manages knowledge")
		}
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
		"root":      map[string]any{"type": "string", "description": "Absolute active project root; use it when calling through a plugin."},
		"action":    map[string]any{"type": "string", "enum": []string{"server.ping", "status", "assistance.status", "assistance.set", "connections", "connect", "disconnect", "mode.set", "retrieval.set", "knowledge.identify", "knowledge.status", "knowledge.upload", "knowledge.get", "knowledge.delete", "community.create", "community.list", "community.get", "community.update", "community.delete", "community.archive", "server.stats", "access.create", "access.list", "access.revoke", "query", "usage", "audit.list"}, "description": "Readers connect, search and read one community. Only the server operator manages content or communities. knowledge.identify maintains local finding IDs and curation timestamps after edits. knowledge.upload replaces changed files automatically. community.delete requires separate human confirmation for locking and final deletion."},
		"retrieval": map[string]any{"type": "string", "enum": []string{"remote"}, "description": "Community retrieval is remote-only. No corpus download or offline cache."},
		"service":   map[string]any{"type": "string", "description": "HTTPS service origin, or local installer origin. Credentials stay outside the project; local owner access is discovered automatically."}, "community": map[string]any{"type": "string", "description": "Community UUID or slug"}, "alias": map[string]any{"type": "string", "description": "Connected project alias"}, "path": map[string]any{"type": "string", "description": "Local output archive path"}, "mode": map[string]any{"type": "string", "enum": []string{"local", "external", "both"}}, "data": map[string]any{"type": "object", "description": "Operation-specific payload. connect accepts invitation for private joining; each project links one community. See the community skill reference."}}, "required": []string{"action"}}
}

func ReadCommandFile(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return os.ReadFile("/dev/stdin")
	}
	return os.ReadFile(path)
}
