package mcp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func serve(t *testing.T, in string) []string {
	t.Helper()
	s := &Server{Name: "t", Version: "1", Instructions: "be a mentor", Tools: []Tool{
		{Name: "ok", Description: "d", Run: func() (string, error) { return "hello", nil }},
		{Name: "bad", Description: "d", Run: func() (string, error) { return "", errors.New("boom") }},
	}}
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(out.String()), "\n")
}

func TestExchanges(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"initialize echoes a known version",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"instructions":"be a mentor","protocolVersion":"2025-06-18","serverInfo":{"name":"t","version":"1"}}}`},
		{"initialize answers latest for an unknown version",
			`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2030-01-01"}}`,
			`{"jsonrpc":"2.0","id":"a","result":{"capabilities":{"tools":{}},"instructions":"be a mentor","protocolVersion":"2025-11-25","serverInfo":{"name":"t","version":"1"}}}`},
		{"initialize without a version", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"initialize needs params.protocolVersion"}}`},
		{"ping", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":2,"result":{}}`},
		{"unknown method", `{"jsonrpc":"2.0","id":3,"method":"nope"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"method not found: nope"}}`},
		{"server/discover falls back to initialize", `{"jsonrpc":"2.0","id":3,"method":"server/discover"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"method not found: server/discover"}}`},
		{"parse error", `{nope`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batches are not supported"}}`},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":4,"method":"ping"}`,
			`{"jsonrpc":"2.0","id":4,"error":{"code":-32600,"message":"invalid request"}}`},
		{"call", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ok"}}`,
			`{"jsonrpc":"2.0","id":5,"result":{"content":[{"text":"hello","type":"text"}],"isError":false}}`},
		{"call that fails is a tool error", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"bad"}}`,
			`{"jsonrpc":"2.0","id":6,"result":{"content":[{"text":"boom","type":"text"}],"isError":true}}`},
		{"unknown tool", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"zzz"}}`,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"unknown tool: zzz"}}`},
		{"call without params", `{"jsonrpc":"2.0","id":8,"method":"tools/call"}`,
			`{"jsonrpc":"2.0","id":8,"error":{"code":-32602,"message":"tools/call needs params.name"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := serve(t, tc.in+"\n")
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestNotificationsGetNoResponse(t *testing.T) {
	in := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n\n" +
		`{"jsonrpc":"2.0","method":"nope"}` + "\n"
	if got := serve(t, in); len(got) != 1 || got[0] != "" {
		t.Fatalf("want no output, got %q", got)
	}
}

func TestToolsList(t *testing.T) {
	got := serve(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")[0]
	for _, want := range []string{`"name":"ok"`, `"inputSchema":{"additionalProperties":false,"type":"object"}`, `"readOnlyHint":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("tools/list lacks %s: %s", want, got)
		}
	}
}

func TestOversizedLineEndsServe(t *testing.T) {
	s := &Server{}
	err := s.Serve(context.Background(), strings.NewReader(strings.Repeat("x", maxLine+10)), &bytes.Buffer{})
	if err == nil {
		t.Fatal("want an error for a line over the limit")
	}
}
