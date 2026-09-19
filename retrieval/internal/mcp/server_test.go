package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/internal/search"
)

func rpc(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	err := Serve(in, &out, "1.0.0-test", func(q string, opts search.QueryOptions) (string, error) {
		return fmt.Sprintf("RESULT for %s kind=%q grade=%q limit=%d", q, opts.Kind, opts.Grade, opts.Limit), nil
	}, func(name string, limit int, root string) (string, error) {
		return fmt.Sprintf("SYMBOL %s limit=%d", name, limit), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestServeLifecycle(t *testing.T) {
	resps := rpc(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"query":"demon joints"}}}`,
	)
	if len(resps) != 3 {
		t.Fatalf("want 3 responses (notification ignored), got %d", len(resps))
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("init: %v", init)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "query" || tools[1].(map[string]any)["name"] != "symbol" {
		t.Fatalf("tools: %v", tools)
	}
	content := resps[2]["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "RESULT for demon joints") {
		t.Fatalf("call result: %q", text)
	}
}

func TestServeKindGradePassthrough(t *testing.T) {
	resps := rpc(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"query":"idle cvar","kind":"fact","grade":"direct","limit":8}}}`,
	)
	content := resps[0]["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != `RESULT for idle cvar kind="fact" grade="direct" limit=8` {
		t.Fatalf("passthrough: %q", text)
	}
}

func TestServeSymbolTool(t *testing.T) {
	resps := rpc(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"symbol","arguments":{"name":"idLangDict_langEntry_t","limit":3}}}`,
	)
	content := resps[0]["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if text != "SYMBOL idLangDict_langEntry_t limit=3" {
		t.Fatalf("symbol passthrough: %q", text)
	}
}

func TestServeUnknownTool(t *testing.T) {
	resps := rpc(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	errObj := resps[0]["error"].(map[string]any)
	if errObj["code"].(float64) != -32602 {
		t.Fatalf("want -32602, got %v", errObj)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	resps := rpc(t, `{"jsonrpc":"2.0","id":9,"method":"nope"}`)
	errObj := resps[0]["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("want -32601, got %v", errObj)
	}
}
