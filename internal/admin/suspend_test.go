package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func activeStatus() LearnerStatus { return LearnerStatus{ID: someID, Username: "diego-daw"} }

func suspendedStatus() LearnerStatus {
	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	return LearnerStatus{ID: someID, Username: "diego-daw", SuspendedAt: &at}
}

func TestLearnerPageLinksTheRightAction(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.learners.view = LearnerView{ID: someID, Username: "d"}
	if body := e.do(http.MethodGet, "/learners/"+someID, s).Body.String(); !strings.Contains(body, "/learners/"+someID+"/suspend\"") || strings.Contains(body, "/unsuspend") {
		t.Fatalf("active page: %s", body)
	}
	at := time.Now()
	e.learners.view.SuspendedAt = &at
	if body := e.do(http.MethodGet, "/learners/"+someID, s).Body.String(); !strings.Contains(body, "/unsuspend\"") {
		t.Fatalf("suspended page: %s", body)
	}
}

func TestLearnerPageShowsHistory(t *testing.T) {
	e := newEnv(t)
	e.learners.view = LearnerView{ID: someID, Username: "d", History: []HistoryRow{
		{At: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), Operator: "ada", Action: SuspendAction, Reason: "abuse <b>x</b>"},
		{At: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Operator: "ada", Action: ViewAction},
	}}
	body := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e)).Body.String()
	for _, want := range []string{"Operator history", "3 Nov 2026 10:00 UTC", "Suspended", "Viewed", "abuse &lt;b&gt;x&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	if strings.Index(body, "Suspended</td>") > strings.Index(body, "Viewed</td>") {
		t.Error("history is not in the given (newest first) order")
	}
}

func TestConfirmPagesStateWhatChanges(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.learners.status = activeStatus()
	rec := e.do(http.MethodGet, "/learners/"+someID+"/suspend", s)
	body := rec.Body.String()
	for _, want := range []string{"What will change", "account_suspended", "still sign out and delete", `name="reason"`, "required", `name="csrf_token"`, `action="/learners/` + someID + `/suspend"`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Errorf("suspend confirm (%d) lacks %q", rec.Code, want)
		}
	}
	e.learners.status = suspendedStatus()
	rec = e.do(http.MethodGet, "/learners/"+someID+"/unsuspend", s)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "work again") || !strings.Contains(body, `action="/learners/`+someID+`/unsuspend"`) {
		t.Fatalf("unsuspend confirm: %d %s", rec.Code, body)
	}
	if len(e.learners.sets) != 0 {
		t.Fatal("a GET changed something")
	}
}

func TestConfirmRefusesTheWrongState(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.learners.status = suspendedStatus()
	rec := e.do(http.MethodGet, "/learners/"+someID+"/suspend", s)
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "<form method") && strings.Contains(rec.Body.String(), "textarea") || !strings.Contains(rec.Body.String(), "already suspended") {
		t.Fatalf("suspend of a suspended: %d %s", rec.Code, rec.Body.String())
	}
	e.learners.status = activeStatus()
	if rec := e.do(http.MethodGet, "/learners/"+someID+"/unsuspend", s); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already active") {
		t.Fatalf("unsuspend of an active: %d", rec.Code)
	}
}

func TestSuspendPostsAndRedirects(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    string
		status  LearnerStatus
		suspend bool
	}{
		{"suspend", "suspend", activeStatus(), true},
		{"unsuspend", "unsuspend", suspendedStatus(), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			s := signedIn(t, e)
			e.learners.status = tt.status
			rec := e.post("/learners/"+someID+"/"+tt.path, url.Values{csrfField: {pageToken(t, e, s)}, "reason": {"  ticket 9  "}}, nil, s)
			wantRedirect(t, rec, "/learners/"+someID)
			want := setCall{"ada", someID, "ticket 9", tt.suspend}
			if len(e.learners.sets) != 1 || e.learners.sets[0] != want {
				t.Fatalf("sets = %+v", e.learners.sets)
			}
		})
	}
}

func TestSuspendRefusals(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		status LearnerStatus
		reason string
		want   int
		hdr    map[string]string
		badTok bool
	}{
		{"missing reason", "suspend", activeStatus(), "", http.StatusBadRequest, nil, false},
		{"blank reason", "suspend", activeStatus(), "   ", http.StatusBadRequest, nil, false},
		{"already suspended", "suspend", suspendedStatus(), "x", http.StatusConflict, nil, false},
		{"already active", "unsuspend", activeStatus(), "x", http.StatusConflict, nil, false},
		{"bad csrf", "suspend", activeStatus(), "x", http.StatusForbidden, nil, true},
		{"cross-site", "suspend", activeStatus(), "x", http.StatusForbidden, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			s := signedIn(t, e)
			e.learners.status = tt.status
			tok := pageToken(t, e, s)
			if tt.badTok {
				tok = "nope"
			}
			rec := e.post("/learners/"+someID+"/"+tt.path, url.Values{csrfField: {tok}, "reason": {tt.reason}}, tt.hdr, s)
			if rec.Code != tt.want || len(e.learners.sets) != 0 {
				t.Fatalf("got %d, sets %+v", rec.Code, e.learners.sets)
			}
		})
	}
}

func TestSuspendReasonTooLongIsRefusedByTheStore(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.learners.status = activeStatus()
	e.learners.setErr = errBadReason
	rec := e.post("/learners/"+someID+"/suspend", url.Values{csrfField: {pageToken(t, e, s)}, "reason": {strings.Repeat("r", 1001)}}, nil, s)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Nothing was changed") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSuspendRaceAndMissingLearner(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	form := func() url.Values { return url.Values{csrfField: {pageToken(t, e, s)}, "reason": {"x"}} }
	e.learners.status, e.learners.setErr = activeStatus(), ErrAlreadyInState
	if rec := e.post("/learners/"+someID+"/suspend", form(), nil, s); rec.Code != http.StatusConflict {
		t.Fatalf("race: %d", rec.Code)
	}
	e.learners.statusErr = ErrNotFound
	if rec := e.post("/learners/"+someID+"/suspend", form(), nil, s); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestSuspendRoutesNeedAnOperator(t *testing.T) {
	e := newEnv(t)
	e.learners.status = activeStatus()
	wantRedirect(t, e.do(http.MethodGet, "/learners/"+someID+"/suspend"), "/signin")
	wantRedirect(t, e.post("/learners/"+someID+"/suspend", url.Values{"reason": {"x"}}, nil), "/signin")
	s := signedIn(t, e)
	e.store.operators[1001].revoked = true
	wantRedirect(t, e.post("/learners/"+someID+"/suspend", url.Values{"reason": {"x"}}, nil, s), "/signin")
	if len(e.learners.sets) != 0 {
		t.Fatal("a non-operator reached the store")
	}
}
