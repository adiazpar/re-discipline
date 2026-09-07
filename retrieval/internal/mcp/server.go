// Package mcp implements a minimal MCP stdio server for re-search:
// newline-delimited JSON-RPC 2.0 exposing the `query` and `symbol`
// tools. Stateless; all knowledge state lives on disk, so any number
// of instances are safe and a killed instance loses nothing.
package mcp

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/adiazpar/re-discipline/retrieval/internal/search"
)

// QueryFunc answers one question with formatted text.
type QueryFunc func(query string, opts search.QueryOptions) (string, error)

// SymbolFunc resolves one symbol name with formatted text.
type SymbolFunc func(name string, limit int) (string, error)

type Extension struct {
	Name        string
	Description string
	Schema      map[string]any
	Call        func(json.RawMessage) (string, error)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads newline-delimited JSON-RPC requests from in until EOF.
func Serve(in io.Reader, out io.Writer, version string, query QueryFunc, symbol SymbolFunc, extensions ...Extension) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue // unparseable line: nothing safe to answer
		}
		if req.ID == nil {
			continue // notification
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "re-search", "version": version},
			}
		case "tools/list":
			resp.Result = map[string]any{"tools": []map[string]any{{
				"name":        "query",
				"description": "Search the project's curated reverse-engineering knowledge base (.re-discipline/docs/). Returns ranked findings with evidence paths. Search here before investigating anything.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":   map[string]any{"type": "string", "description": "natural-language question or identifier"},
						"limit":   map[string]any{"type": "integer", "description": "max results (default 8)"},
						"kind":    map[string]any{"type": "string", "description": "only docs of this kind (fact|ops|reference); omit for all"},
						"grade":   map[string]any{"type": "string", "description": "only docs of this grade (direct|inferred|reported); omit for all"},
						"sources": map[string]any{"type": "string", "enum": []string{"local", "external", "both"}},
						"offline": map[string]any{"type": "boolean", "description": "Use cached community knowledge without network requests"},
					},
					"required": []string{"query"},
				},
			}, {
				"name":        "symbol",
				"description": "Look up one symbol (struct layout, constant, group) by exact name from the project's symbol table (.re-discipline/symbols.jsonl). Case-insensitive; falls back to substring matching when nothing matches exactly. Use for type layouts, field offsets, and constant values; use `query` for questions.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "description": "symbol name, e.g. idLangDict_langEntry_t or TAG_LANGDICT"},
						"limit": map[string]any{"type": "integer", "description": "max results (default 5)"},
					},
					"required": []string{"name"},
				},
			}}}
			listed := resp.Result.(map[string]any)
			for _, ext := range extensions {
				listed["tools"] = append(listed["tools"].([]map[string]any), map[string]any{"name": ext.Name, "description": ext.Description, "inputSchema": ext.Schema})
			}
		case "tools/call":
			var p struct {
				Name      string `json:"name"`
				Arguments struct {
					Query   string `json:"query"`
					Name    string `json:"name"`
					Limit   int    `json:"limit"`
					Kind    string `json:"kind"`
					Grade   string `json:"grade"`
					Sources string `json:"sources"`
					Offline bool   `json:"offline"`
				} `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			var generic struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(req.Params, &generic)
			var text string
			var err error
			switch p.Name {
			case "query":
				text, err = query(p.Arguments.Query, search.QueryOptions{
					Limit:   p.Arguments.Limit,
					Kind:    p.Arguments.Kind,
					Grade:   p.Arguments.Grade,
					Sources: p.Arguments.Sources,
					Offline: p.Arguments.Offline,
				})
			case "symbol":
				text, err = symbol(p.Arguments.Name, p.Arguments.Limit)
			default:
				found := false
				for _, ext := range extensions {
					if ext.Name == p.Name {
						found = true
						text, err = ext.Call(generic.Arguments)
						break
					}
				}
				if !found {
					resp.Error = &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
				}
			}
			if resp.Error != nil {
				break
			}
			if err != nil {
				resp.Result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": p.Name + " error: " + err.Error()}},
					"isError": true,
				}
				break
			}
			resp.Result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			}
		case "ping":
			resp.Result = map[string]any{}
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}
