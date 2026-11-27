//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type onboardingBody struct {
	NextStep     string `json:"next_step"`
	Env          string `json:"env"`
	PublicOrigin string `json:"public_origin"`
	Path         struct {
		Slug   *string `json:"slug"`
		Chosen bool    `json:"chosen"`
	} `json:"path"`
	HostedDefault bool     `json:"hosted_default"`
	CLIConnected  bool     `json:"cli_connected"`
	Waitlist      []string `json:"waitlist"`
	Checks        []struct {
		ID, Group, Status, Text, Source string
		CheckedAt                       string `json:"checked_at"`
	} `json:"checks"`
	AgentsMD    string `json:"agents_md"`
	FirstTicket *struct {
		ID, Title   string
		State       *string `json:"state"`
		CommitState *string `json:"commit_state"`
	} `json:"first_ticket"`
}

func readOnboarding(t *testing.T, code int, body string) onboardingBody {
	t.Helper()
	var o onboardingBody
	if code != 200 || json.Unmarshal([]byte(body), &o) != nil {
		t.Fatalf("onboarding: %d %s", code, body)
	}
	return o
}

func allChecks(ids ...string) []map[string]string {
	var out []map[string]string
	for _, id := range ids {
		out = append(out, dc(id, "ok", id+" ok"))
	}
	return out
}

func TestOnboardingListsTheWaitlist(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	seedExtraPath(t)
	asOwner(t, "INSERT INTO paths (org_id, slug, title, status, position) SELECT id, 'backend', 'Backend', 'planned', 4 FROM orgs WHERE slug = 'public'")
	resetLearners(t, "1001")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	list := func() string {
		code, body := ada.getJSON("/v1/onboarding")
		if o := readOnboarding(t, code, body); o.Waitlist == nil {
			t.Fatalf("waitlist is null: %s", body)
		} else {
			return strings.Join(o.Waitlist, ",")
		}
		return ""
	}
	if got := list(); got != "" {
		t.Fatalf("empty: %q", got)
	}
	ada.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	ada.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"backend"}`)
	if got := list(); got != "backend,security" {
		t.Fatalf("joined: %q", got)
	}
	ada.mustSend("DELETE", "/v1/onboarding/waitlist/backend", "")
	if got := list(); got != "security" {
		t.Fatalf("left: %q", got)
	}
	asOwner(t, "UPDATE paths SET status = 'available' WHERE slug = 'security'")
	if got := list(); got != "" {
		t.Fatalf("became available: %q", got)
	}
	if waitlisted(t, "1001") != "security" {
		t.Fatal("the row must stay when the path becomes available")
	}
}

func TestOnboardingEveryStepIsReachable(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	if code, _ := newBrowser(t).getJSON("/v1/onboarding"); code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", code)
	}
	resp, err := ada.c.Get(apiURL(t) + "/v1/onboarding")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control: %q", resp.Header.Get("Cache-Control"))
	}

	step := func() onboardingBody {
		code, body := ada.getJSON("/v1/onboarding")
		return readOnboarding(t, code, body)
	}

	o := step()
	if o.NextStep != "o0" || o.Path.Chosen || o.CLIConnected || o.Env == "" || o.PublicOrigin == "" || !strings.Contains(o.AgentsMD, "#") ||
		len(o.Checks) != 0 || o.FirstTicket == nil || o.FirstTicket.ID != "foundations/a" {
		t.Fatalf("o0: %+v", o)
	}

	asOwner(t, "UPDATE learners SET chosen_path_id = (SELECT id FROM paths WHERE slug = 'systems-devops') WHERE github_id = 1001")
	if o = step(); o.NextStep != "o2" || !o.Path.Chosen || o.Path.Slug == nil || *o.Path.Slug != "systems-devops" {
		t.Fatalf("o2: %+v", o)
	}

	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	if o = step(); o.NextStep != "o3" || !o.CLIConnected {
		t.Fatalf("o3: %+v", o)
	}
	// The bearer reads the same thing.
	code, body, _ := getAs(t, tok, "/v1/onboarding")
	if b := readOnboarding(t, code, body); b.NextStep != "o3" {
		t.Fatalf("bearer: %+v", b)
	}

	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:00:00Z", allChecks("memory", "disk", "cpu", "virtualization", "vm_manager", "git")...))
	o = step()
	if o.NextStep != "o4" || len(o.Checks) != 6 || o.Checks[0].ID != "cpu" || o.Checks[0].Group != "machine" || o.Checks[0].Source != "cli" ||
		!strings.HasPrefix(o.Checks[0].CheckedAt, "2026-11-24T09:00:00") {
		t.Fatalf("o4: %+v", o)
	}

	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:01:00Z", allChecks("ssh_key", "git_signing", "key_signing", "key_auth", "repo", "ruleset", "agents_md")...))
	if o = step(); o.NextStep != "o5" || o.FirstTicket == nil || o.FirstTicket.ID != "foundations/a" || o.FirstTicket.Title == "" {
		t.Fatalf("o5: %+v", o)
	}

	// Started tickets do not finish onboarding; a passed one does, and it sticks.
	// first_ticket never moves: it follows the learner's progress on the path's first ticket.
	first := func(state, commit string) {
		t.Helper()
		o := step()
		ft := o.FirstTicket
		got := func(p *string) string {
			if p == nil {
				return "null"
			}
			return *p
		}
		if ft == nil || ft.ID != "foundations/a" || got(ft.State) != state || got(ft.CommitState) != commit {
			t.Fatalf("first_ticket want %s/%s: %+v", state, commit, ft)
		}
	}
	first("null", "null")
	setProgress(t, "foundations/a", "started", "running")
	first("started", "null")
	if o = step(); o.NextStep != "o5" {
		t.Fatalf("started: %+v", o)
	}
	asOwner(t, "UPDATE ticket_progress SET state = 'passed', first_passed_at = now(), passed_commit = repeat('a', 40), commit_state = 'pending', commit_at = now() WHERE item_id = 'foundations/a'")
	first("passed", "pending")
	asOwner(t, "UPDATE ticket_progress SET commit_state = 'verified' WHERE item_id = 'foundations/a'")
	first("passed", "verified")
	if o = step(); o.NextStep != "done" {
		t.Fatalf("done: %+v", o)
	}
	asOwner(t, "UPDATE cli_tokens SET revoked_at = now()")
	asOwner(t, "DELETE FROM doctor_results")
	if o = step(); o.NextStep != "done" || o.CLIConnected {
		t.Fatalf("done must stick: %+v", o)
	}
}

func TestOnboardingRevisitIsDone(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	ada := newBrowser(t)
	ada.signIn("ada", "", "age=on&terms=on", false)
	setProgress(t, "foundations/a", "revisit", "")
	code, body := ada.getJSON("/v1/onboarding")
	if o := readOnboarding(t, code, body); o.NextStep != "done" {
		t.Fatalf("revisit: %+v", o)
	}
}

func TestLoginEventArrivesOnTheOpenStream(t *testing.T) {
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	signUp(t, "bob")
	ada := newBrowser(t)
	ada.signIn("ada", "", "age=on&terms=on", false)
	bob := newBrowser(t)
	bob.signIn("bob", "", "age=on&terms=on", false)
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)
	other := openStream(t, apiURL(t), cookieOf(t, bob))
	other.next("ready", 5*time.Second)

	tok := cliToken(t, newCLIClient(t), "ada")
	ev := s.next("login", 5*time.Second)
	var m map[string]string
	if json.Unmarshal([]byte(ev.data), &m) != nil || len(m) != 4 || m["device_name"] != "integration laptop" || m["os"] != "linux/amd64" ||
		m["cli_version"] != "v0.0.0-test" || m["at"] == "" || strings.Contains(ev.data, tok) || strings.Contains(ev.data, "hash") {
		t.Fatalf("login frame: %s", ev.data)
	}
	other.quiet(500 * time.Millisecond)
}
