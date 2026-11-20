// Package mcp is a minimal Model Context Protocol server over stdio: newline-delimited
// JSON-RPC 2.0, tools only, no dependencies. It speaks the initialize handshake
// revisions (2025-11-25 and earlier). The stateless 2026-07-28 revision is not
// implemented: its clients probe server/discover, get -32601 and fall back to initialize.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Versions the server speaks, newest first.
var supported = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
)

const maxLine = 4 << 20

// Tool is a read-only tool without arguments. Run returns the text shown to the model.
type Tool struct {
	Name        string
	Description string
	Run         func() (text string, err error)
}

// Server answers MCP requests for a fixed set of tools.
type Server struct {
	Name, Version, Instructions string
	Tools                       []Tool
	// Log gets diagnostics; nil discards them. Never stdout, which carries the protocol.
	Log io.Writer
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from r and writes responses to w until r ends or ctx is done.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), maxLine)
		for sc.Scan() {
			select {
			case lines <- bytes.Clone(sc.Bytes()):
			case <-ctx.Done():
				return
			}
		}
		scanErr <- sc.Err()
	}()
	enc := json.NewEncoder(w) // appends the newline
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return <-scanErr
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if resp := s.handle(line); resp != nil {
				if err := enc.Encode(resp); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Server) logf(format string, a ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", a...)
	}
}

func fail(id json.RawMessage, code int, msg string) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// handle returns nil for notifications, which get no response.
func (s *Server) handle(line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		var arr []json.RawMessage
		if json.Unmarshal(line, &arr) == nil { // batches are gone since 2025-06-18
			return fail(json.RawMessage("null"), codeInvalidRequest, "batches are not supported")
		}
		return fail(json.RawMessage("null"), codeParse, "parse error")
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return fail(idOrNull(req.ID), codeInvalidRequest, "invalid request")
	}
	if len(req.ID) == 0 { // a notification: notifications/initialized, notifications/cancelled...
		return nil
	}
	switch req.Method {
	case "initialize":
		return s.initialize(req)
	case "ping":
		return &response{JSONRPC: "2.0", ID: req.ID, Result: struct{}{}}
	case "tools/list":
		return &response{JSONRPC: "2.0", ID: req.ID, Result: s.list()}
	case "tools/call":
		return s.call(req)
	}
	return fail(req.ID, codeNoMethod, "method not found: "+req.Method)
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func (s *Server) initialize(req request) *response {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ProtocolVersion == "" {
		return fail(req.ID, codeInvalidParams, "initialize needs params.protocolVersion")
	}
	version := supported[0]
	for _, v := range supported {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	s.logf("initialize: client asked %s, answering %s", p.ProtocolVersion, version)
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
	}
	if s.Instructions != "" {
		result["instructions"] = s.Instructions
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (s *Server) list() any {
	tools := make([]map[string]any, 0, len(s.Tools))
	for _, t := range s.Tools {
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		})
	}
	return map[string]any{"tools": tools}
}

func (s *Server) call(req request) *response {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return fail(req.ID, codeInvalidParams, "tools/call needs params.name")
	}
	for _, t := range s.Tools {
		if t.Name != p.Name {
			continue
		}
		text, err := t.Run()
		isErr := err != nil
		if isErr {
			s.logf("tool %s: %v", t.Name, err)
			text = err.Error()
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": isErr,
		}}
	}
	return fail(req.ID, codeInvalidParams, "unknown tool: "+p.Name)
}
