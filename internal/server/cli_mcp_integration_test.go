//go:build integration

package server_test

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kibanbuild/dojo/internal/cli"
	"github.com/kibanbuild/dojo/internal/state"
)

// dojo mcp reads the hints the learner opened on the web, through the API with the CLI token.
func TestCLIMCPHintsOpenedReadsTheAPIIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	if err := state.SaveToken(tok); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load()
	st.Current, st.HintsShown = "foundations/a", 0 // the local counter never moves for web hints
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	tickets := fstest.MapFS{"foundations/a/ticket.yaml": {Data: []byte(
		"id: foundations/a\ntitle: Find the log\nrole: foundations\norder: 1\nest_minutes: 10\nbrief: Find it.\nhints: [hint one, hint two, hint three]\n")},
		"foundations/a/setup.sh": {Data: []byte("true\n")}, "foundations/a/check.sh": {Data: []byte("true\n")}}

	call := func() string {
		var out bytes.Buffer
		app := &cli.App{
			Out: &out, Err: &bytes.Buffer{}, Now: time.Now, Catalog: tickets,
			In:    strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hints_opened"}}` + "\n"),
			HTTP:  &http.Client{Timeout: 15 * time.Second},
			Login: cli.LoginConfig{APIURL: apiURL(t)},
		}
		if err := app.Run(context.Background(), []string{"mcp"}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if got := call(); !strings.Contains(got, "not opened any hint") {
		t.Fatalf("before opening: %s", got)
	}
	if code, body := openHint(c, tok, "foundations/a", "1"); code != http.StatusOK {
		t.Fatalf("open hint: %d %q", code, body)
	}
	got := call()
	if !strings.Contains(got, "Hint 1/3: hint one") || strings.Contains(got, "hint two") || strings.Contains(got, "hint three") {
		t.Fatalf("after opening hint 1: %s", got)
	}
}
