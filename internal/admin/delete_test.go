package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLearnerPageLinksDelete(t *testing.T) {
	e := newEnv(t)
	e.learners.view = LearnerView{ID: someID, Username: "d"}
	body := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e)).Body.String()
	if !strings.Contains(body, `href="/learners/`+someID+`/delete"`) || !strings.Contains(body, "btn-quiet-danger") {
		t.Fatalf("no delete link: %s", body)
	}
}

func TestDeleteConfirmStatesWhatHappensAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.learners.status = activeStatus()
	rec := e.do(http.MethodGet, "/learners/"+someID+"/delete", signedIn(t, e))
	body := rec.Body.String()
	for _, want := range []string{"Delete @diego-daw", "What will change", "signed out everywhere", "goodbye email", "arrive with the exam work later",
		`name="reason"`, `name="typed"`, `name="csrf_token"`, `action="/learners/` + someID + `/delete"`, "btn btn-danger", "Delete account"} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Errorf("delete confirm (%d) lacks %q", rec.Code, want)
		}
	}
	if strings.Contains(body, "style=") || len(e.learners.deletes) != 0 {
		t.Fatal("inline style or a GET deleted something")
	}
}

func TestDeletePostsAndRedirectsToSearch(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.learners.status = activeStatus()
	rec := e.post("/learners/"+someID+"/delete", url.Values{csrfField: {pageToken(t, e, s)}, "reason": {"  erasure by email  "}, "typed": {" diego-daw "}}, nil, s)
	wantRedirect(t, rec, "/learners?deleted=1")
	if len(e.learners.deletes) != 1 || e.learners.deletes[0] != (setCall{Op: "ada", ID: someID, Reason: "erasure by email"}) {
		t.Fatalf("deletes = %+v", e.learners.deletes)
	}
	body := e.do(http.MethodGet, "/learners?deleted=1", s).Body.String()
	if !strings.Contains(body, "Account deleted.") || strings.Contains(body, "diego") {
		t.Fatalf("notice: %s", body)
	}
}

func TestDeleteRefusals(t *testing.T) {
	tests := []struct {
		name          string
		reason, typed string
		want          int
		hdr           map[string]string
		badTok        bool
	}{
		{"missing reason", "", "diego-daw", http.StatusBadRequest, nil, false},
		{"blank reason", "   ", "diego-daw", http.StatusBadRequest, nil, false},
		{"reason too long", strings.Repeat("r", 1001), "diego-daw", http.StatusBadRequest, nil, false},
		{"missing typed username", "x", "", http.StatusBadRequest, nil, false},
		{"wrong typed username", "x", "diego", http.StatusBadRequest, nil, false},
		{"typed username in another case", "x", "Diego-Daw", http.StatusBadRequest, nil, false},
		{"bad csrf", "x", "diego-daw", http.StatusForbidden, nil, true},
		{"cross-site", "x", "diego-daw", http.StatusForbidden, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			s := signedIn(t, e)
			e.learners.status = activeStatus()
			tok := pageToken(t, e, s)
			if tt.badTok {
				tok = "nope"
			}
			rec := e.post("/learners/"+someID+"/delete", url.Values{csrfField: {tok}, "reason": {tt.reason}, "typed": {tt.typed}}, tt.hdr, s)
			if rec.Code != tt.want || len(e.learners.deletes) != 0 {
				t.Fatalf("got %d, deletes %+v", rec.Code, e.learners.deletes)
			}
			if tt.want == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "Nothing was changed") {
				t.Fatalf("no explanation: %s", rec.Body.String())
			}
		})
	}
}

func TestDeleteMissingLearnerAndStoreAnswers(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	form := func() url.Values {
		return url.Values{csrfField: {pageToken(t, e, s)}, "reason": {"x"}, "typed": {"diego-daw"}}
	}
	e.learners.statusErr = ErrNotFound
	if rec := e.post("/learners/"+someID+"/delete", form(), nil, s); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/learners/"+someID+"/delete", s); rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing: %d", rec.Code)
	}
	e.learners.statusErr, e.learners.status, e.learners.delErr = nil, activeStatus(), ErrNotFound
	if rec := e.post("/learners/"+someID+"/delete", form(), nil, s); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted meanwhile: %d", rec.Code)
	}
	e.learners.delErr = errBadReason
	if rec := e.post("/learners/"+someID+"/delete", form(), nil, s); rec.Code != http.StatusBadRequest {
		t.Fatalf("store refused reason: %d", rec.Code)
	}
}

func TestDeleteRoutesNeedAnOperator(t *testing.T) {
	e := newEnv(t)
	e.learners.status = activeStatus()
	wantRedirect(t, e.do(http.MethodGet, "/learners/"+someID+"/delete"), "/signin")
	wantRedirect(t, e.post("/learners/"+someID+"/delete", url.Values{"reason": {"x"}, "typed": {"diego-daw"}}, nil), "/signin")
	if len(e.learners.deletes) != 0 {
		t.Fatal("a non-operator reached the store")
	}
}
