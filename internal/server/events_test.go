package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

type fakeEvents struct {
	got []repo.EventIn
	res repo.EventsResult
	err error
}

func (f *fakeEvents) RecordEvents(_ context.Context, _ pgtype.UUID, evs []repo.EventIn) (repo.EventsResult, error) {
	f.got = evs
	return f.res, f.err
}

const (
	evID1 = "0194f0f2-6c1e-7d3a-9b7e-2f4a1c8e5d10"
	evID2 = "0194f0f2-9a40-7b11-8c02-51d3e7a9f6b2"
)

func started(id string) string {
	return `{"id":"` + id + `","type":"ticket.started","at":"2026-11-23T10:15:02Z","item_id":"support-l1/perms-03","lab":"local","catalog_version":"a1b2c3d4e5f6"}`
}

func check(id, extra string) string {
	return `{"id":"` + id + `","type":"check.run","at":"2026-11-23T10:31:40Z","item_id":"support-l1/perms-03","result":"fail","duration_ms":2210` + extra + `}`
}

func postEvents(f *fakeEvents, store *fakeSessions, header, ctype, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: store, Events: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestEventsValidation(t *testing.T) {
	big := strings.Repeat("x", 20_000)
	tests := []struct {
		name, body string
		want       string // response body, empty = accepted
	}{
		{"ok", `{"events":[` + started(evID1) + `,` + check(evID2, `,"passed":3,"total":5,"output":"boom"`) + `]}`, ""},
		{"empty batch", `{"events":[]}`, `{"error":"bad_request"}`},
		{"not json", `nope`, `{"error":"bad_request"}`},
		{"unknown top field", `{"events":[` + started(evID1) + `],"x":1}`, `{"error":"bad_request"}`},
		{"unknown event field", `{"events":[` + started(evID1) + `,` + check(evID2, `,"extra":1`) + `]}`, `"index":1`},
		{"hint.opened", `{"events":[{"id":"` + evID1 + `","type":"hint.opened","at":"2026-11-23T10:15:02Z","item_id":"a/b"}]}`, `"index":0`},
		{"unknown type", `{"events":[{"id":"` + evID1 + `","type":"x","at":"2026-11-23T10:15:02Z","item_id":"a/b"}]}`, `"index":0`},
		{"field of another type", `{"events":[` + started(evID1) + `,{"id":"` + evID2 + `","type":"lab.started","at":"2026-11-23T10:15:02Z","item_id":"a/b","result":"pass"}]}`, `"index":1`},
		{"bad id", `{"events":[` + strings.Replace(started(evID1), evID1, "not-a-uuid", 1) + `]}`, `"index":0`},
		{"at not UTC", `{"events":[` + strings.Replace(started(evID1), "10:15:02Z", "10:15:02+01:00", 1) + `]}`, `"index":0`},
		{"bad lab", `{"events":[` + strings.Replace(started(evID1), `"local"`, `"cloud"`, 1) + `]}`, `"index":0`},
		{"bad catalog version", `{"events":[` + strings.Replace(started(evID1), "a1b2c3d4e5f6", "short", 1) + `]}`, `"index":0`},
		{"bad result", `{"events":[` + strings.Replace(check(evID1, ""), `"fail"`, `"maybe"`, 1) + `]}`, `"index":0`},
		{"passed without total", `{"events":[` + check(evID1, `,"passed":1`) + `]}`, `"index":0`},
		{"passed over total", `{"events":[` + check(evID1, `,"passed":6,"total":5`) + `]}`, `"index":0`},
		{"duration over a day", `{"events":[` + strings.Replace(check(evID1, ""), "2210", "86400001", 1) + `]}`, `"index":0`},
		{"output over 16384 bytes", `{"events":[` + check(evID1, `,"output":"`+big+`"`) + `]}`, `"index":0`},
		{"output over 16384 bytes once NULs become U+FFFD", `{"events":[` + check(evID1, `,"output":"`+strings.Repeat(`\u0000`, 5462)+`"`) + `]}`, `"index":0`},
		{"bad lab.failed reason", `{"events":[{"id":"` + evID1 + `","type":"lab.failed","at":"2026-11-23T10:15:02Z","item_id":"a/b","reason":"fire"}]}`, `"index":0`},
		{"item id with one part", `{"events":[` + strings.Replace(started(evID1), "support-l1/perms-03", "single", 1) + `]}`, `"index":0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvents{res: repo.EventsResult{Accepted: 1}}
			rec := postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json", tt.body)
			if tt.want == "" {
				if rec.Code != http.StatusOK || len(f.got) == 0 {
					t.Fatalf("got %d %q", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tt.want) || f.got != nil {
				t.Fatalf("got %d %q, stored %d", rec.Code, rec.Body.String(), len(f.got))
			}
		})
	}
}

func TestEventsBatchLimits(t *testing.T) {
	var evs []string
	for i := range 51 {
		evs = append(evs, strings.Replace(started(evID1), "d10", string(rune('a'+i%6))+"1"+string(rune('0'+i%10)), 1))
	}
	f := &fakeEvents{}
	rec := postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json", `{"events":[`+strings.Join(evs, ",")+`]}`)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "index") {
		t.Fatalf("51 events: %d %q", rec.Code, rec.Body.String())
	}
	// 64 KB per event on the wire, even when output is within its own cap.
	pad := `,"output":"` + strings.Repeat("y", 16384) + `"`
	huge := check(evID1, pad) + strings.Repeat(" ", 0)
	huge = strings.Replace(huge, `"duration_ms":2210`, `"duration_ms":2210`+strings.Repeat(" ", 50_000), 1)
	rec = postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json", `{"events":[`+huge+`]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"index":0`) {
		t.Fatalf("event over 64 KB: %d %q", rec.Code, rec.Body.String())
	}
	// Over 1 MB of body.
	rec = postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json", `{"events":[`+strings.Repeat(" ", 1<<20)+started(evID1)+`]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("body over 1 MB: %d", rec.Code)
	}
}

func TestEventsAuthAndAnswer(t *testing.T) {
	body := `{"events":[` + started(evID1) + `]}`
	good := &fakeSessions{cli: activeCLI(time.Now(), false)}
	for _, tt := range []struct {
		name, header, ctype string
		store               *fakeSessions
		want                int
	}{
		{"no credential", "", "application/json", &fakeSessions{}, 401},
		{"suspended", "Bearer " + goodCLIToken, "application/json", &fakeSessions{cli: activeCLI(time.Now(), true)}, 403},
		{"wrong content type", "Bearer " + goodCLIToken, "text/plain", good, 400},
		{"missing content type", "Bearer " + goodCLIToken, "", good, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvents{}
			if rec := postEvents(f, tt.store, tt.header, tt.ctype, body); rec.Code != tt.want || f.got != nil {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
		})
	}

	f := &fakeEvents{res: repo.EventsResult{Accepted: 2, Duplicates: 1, Ignored: 1}}
	rec := postEvents(f, good, "Bearer "+goodCLIToken, "application/json; charset=utf-8", body)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"accepted":2,"duplicates":1,"ignored":1}` {
		t.Fatalf("answer: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(string(f.got[0].Payload), `"id"`) || !strings.Contains(string(f.got[0].Payload), `"catalog_version":"a1b2c3d4e5f6"`) {
		t.Fatalf("payload: %s", f.got[0].Payload)
	}

	f = &fakeEvents{err: errors.New("db down")}
	if rec := postEvents(f, good, "Bearer "+goodCLIToken, "application/json", body); rec.Code != 500 || !strings.Contains(rec.Body.String(), "server_error") {
		t.Fatalf("store failure: %d %q", rec.Code, rec.Body.String())
	}
	f = &fakeEvents{err: repo.ErrLearnerGone}
	if rec := postEvents(f, good, "Bearer "+goodCLIToken, "application/json", body); rec.Code != 401 {
		t.Fatalf("deleted learner: %d", rec.Code)
	}
}

func TestEventsCookieIs401AndOutputNeverInPayload(t *testing.T) {
	f := &fakeEvents{}
	req := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader(`{"events":[`+started(evID1)+`]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: &fakeSessions{row: activeRow(time.Now(), false)}, Events: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != 401 || f.got != nil {
		t.Fatalf("cookie: %d %q", rec.Code, rec.Body.String())
	}

	f = &fakeEvents{}
	postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json", `{"events":[`+check(evID1, `,"output":"SECRET"`)+`]}`)
	if len(f.got) != 1 || strings.Contains(string(f.got[0].Payload), "SECRET") || f.got[0].Output == nil || *f.got[0].Output != "SECRET" {
		t.Fatalf("payload %s", f.got[0].Payload)
	}
}

func TestEventsRateLimitIsPerLearnerAndBearerOnly(t *testing.T) {
	lim := testLimits(1)
	lim.Events = newMemLimiter(1, time.Minute, 100, time.Now)
	f := &fakeEvents{}
	h := New(fakeReady{}, Auth{Store: &fakeSessions{cli: activeCLI(time.Now(), false)}, Events: f}, lim, nil, nil)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader(`{"events":[`+started(evID1)+`]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+goodCLIToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(); rec.Code != 200 {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := call(); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second: %d", rec.Code)
	}
}

func TestEventsNULBecomesReplacementCharacter(t *testing.T) {
	f := &fakeEvents{res: repo.EventsResult{Accepted: 2}}
	lf := `{"id":"` + evID2 + `","type":"lab.failed","at":"2026-11-23T10:15:02Z","item_id":"a/b","reason":"vm","detail":"x\u0000y"}`
	rec := postEvents(f, &fakeSessions{cli: activeCLI(time.Now(), false)}, "Bearer "+goodCLIToken, "application/json",
		`{"events":[`+check(evID1, `,"output":"a\u0000b"`)+`,`+lf+`]}`)
	if rec.Code != 200 || len(f.got) != 2 || *f.got[0].Output != "a�b" || !strings.Contains(string(f.got[1].Payload), `x�y`) {
		t.Fatalf("%d %q %+v", rec.Code, rec.Body.String(), f.got)
	}
}
