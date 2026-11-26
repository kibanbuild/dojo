package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kibanbuild/dojo/internal/state"
)

func TestBadEventIsDroppedAndTheRestIsKept(t *testing.T) {
	var mu sync.Mutex
	var accepted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		for i, ev := range body.Events {
			if ev["type"] == "ticket.commit" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":"bad_request","index":%d}`, i)
				return
			}
		}
		for _, ev := range body.Events {
			accepted = append(accepted, ev["type"].(string))
		}
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t)
	e.app.Login = LoginConfig{APIURL: srv.URL}
	e.app.Getenv = func(string) string { return "" }
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"lab.started", "ticket.commit", "lab.stopped"} {
		e.app.recordEvent(state.ProgressEvent{Type: typ, ItemID: "white/a"})
	}
	e.app.flushEvents(context.Background())
	mu.Lock()
	got := append([]string(nil), accepted...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "lab.started" || got[1] != "lab.stopped" {
		t.Errorf("accepted %v, want the two events around the rejected one", got)
	}
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Errorf("queue = %d events, want none", len(q.Events))
	}
}

func TestBadIndexIgnoresUnusableAnswers(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"index":-1}`, `{"index":3}`, `nope`} {
		if got := badIndex([]byte(body), 3); got != -1 {
			t.Errorf("badIndex(%q) = %d", body, got)
		}
	}
	if got := badIndex([]byte(`{"index":2}`), 3); got != 2 {
		t.Errorf("badIndex = %d", got)
	}
}
