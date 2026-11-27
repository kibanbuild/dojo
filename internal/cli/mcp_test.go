package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kibanyadev/dojo/internal/state"
)

// mcpCall runs one tools/call through the real dojo mcp command and returns the result text.
func mcpCall(t *testing.T, e *env, tool string) string {
	t.Helper()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `"}}` + "\n"
	e.app.In = strings.NewReader(in)
	if err := e.run(t, "mcp"); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
	}
	if err := json.Unmarshal(e.out.Bytes(), &resp); err != nil {
		t.Fatalf("bad response %q: %v", e.out.String(), err)
	}
	if resp.Result.IsError || len(resp.Result.Content) != 1 {
		t.Fatalf("unexpected result: %s", e.out.String())
	}
	return resp.Result.Content[0].Text
}

func TestMCPWithoutATicketSaysToStartOne(t *testing.T) {
	e := newEnv(t)
	for _, tool := range []string{"current_ticket", "last_check", "hints_opened"} {
		if got := mcpCall(t, e, tool); !strings.Contains(got, "dojo start <id>") {
			t.Errorf("%s: %q does not say to run dojo start", tool, got)
		}
	}
}

func TestMCPToolsShowOnlyWhatTheLearnerSaw(t *testing.T) {
	e := newEnv(t)
	st, _ := state.Load()
	st.Current, st.HintsShown = "white/a", 1
	st.SetLastCheck([]byte("PASS  one\nFAIL  two\n"))
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mcpCall(t, e, "current_ticket"); !strings.Contains(got, "white/a: Ticket A") || !strings.Contains(got, "Fix A.") {
		t.Errorf("current_ticket = %q", got)
	}
	if got := mcpCall(t, e, "last_check"); !strings.Contains(got, "FAIL  two") {
		t.Errorf("last_check = %q", got)
	}
	got := mcpCall(t, e, "hints_opened")
	if !strings.Contains(got, "Hint 1/2: one") || strings.Contains(got, "two") {
		t.Errorf("hints_opened = %q, want only the opened hint", got)
	}
}

func TestMCPHintsOpenedAfterDojoHint(t *testing.T) {
	e := newEnv(t)
	st, _ := state.Load()
	st.Current = "white/a"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mcpCall(t, e, "hints_opened"); !strings.Contains(got, "not opened any hint") {
		t.Errorf("before: %q", got)
	}
	if err := e.run(t, "hint"); err != nil {
		t.Fatal(err)
	}
	if got := mcpCall(t, e, "hints_opened"); !strings.Contains(got, "Hint 1/2: one") {
		t.Errorf("after dojo hint: %q", got)
	}
}

func TestMCPNeverExposesSolutionContent(t *testing.T) {
	secrets := map[string]string{
		"setup.sh": "SECRET-SETUP-9f1", "check.sh": "SECRET-CHECK-3c7",
		"solution.sh": "SECRET-SOLUTION-b42", "wrong-x.sh": "SECRET-WRONG-d05",
	}
	cat := fstest.MapFS{"white/s/ticket.yaml": {Data: []byte("id: white/s\ntitle: S\nrole: white\norder: 1\nest_minutes: 5\nbrief: Fix S.\nhints: [h1, h2, h3]\n")}}
	for f, s := range secrets {
		cat["white/s/"+f] = &fstest.MapFile{Data: []byte(s)}
	}
	e := newEnv(t)
	e.app.Catalog = cat
	st, _ := state.Load()
	st.Current, st.HintsShown = "white/s", 3
	st.SetLastCheck([]byte("FAIL  something"))
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	e.app.In = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	if err := e.run(t, "mcp"); err != nil {
		t.Fatal(err)
	}
	all.Write(e.out.Bytes())
	for _, tool := range []string{"current_ticket", "last_check", "hints_opened"} {
		all.WriteString(mcpCall(t, e, tool))
	}
	for f, s := range secrets {
		if strings.Contains(all.String(), s) {
			t.Errorf("an MCP response carries the content of %s", f)
		}
	}
	if !strings.Contains(all.String(), "Fix S.") {
		t.Fatal("the test saw no ticket text, so it proved nothing")
	}
}

func TestMCPLogsGoToStderr(t *testing.T) {
	e := newEnv(t)
	var errOut bytes.Buffer
	e.app.Err = &errOut
	e.app.In = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}` + "\n")
	if err := e.app.Run(context.Background(), []string{"mcp"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "initialize") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if strings.Count(e.out.String(), "\n") != 1 {
		t.Errorf("stdout must hold exactly the one response, got %q", e.out.String())
	}
}
