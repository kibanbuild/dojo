package fakegithub

import (
	"crypto/sha1" //nolint:gosec // git blob ids are SHA-1; this is not a security use
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateWindow is GitHub's anonymous rate-limit window.
const rateWindow = time.Hour

// Key is one entry of GET /users/{login}/keys or /ssh_signing_keys.
type Key struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Title     string `json:"title,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Rule is one entry of GET /repos/{owner}/{repo}/rules/branches/{branch}.
type Rule struct {
	Type      string `json:"type"`
	RulesetID int64  `json:"ruleset_id"`
}

// Repo is a seeded repository. Only a public repo is visible: a private one is a 404 to an anonymous caller.
type Repo struct {
	Name          string            `json:"name"`
	Private       bool              `json:"private"`
	DefaultBranch string            `json:"default_branch"` // empty means main
	Rules         map[string][]Rule `json:"rules"`          // by branch
	Files         map[string]string `json:"files"`          // path to content
}

// account is the mutable read-side data of one user, behind readStore.mu.
type account struct {
	id            int64
	login         string // the current login: a test can rename the account
	keys, signing []Key
	repos         map[string]Repo
}

type readStore struct {
	mu       sync.Mutex
	seed     []User
	accounts map[string]*account // by lower-case login
	hits     map[string]*window  // by remote IP
	// logins is the current login by account id once a test renames an account.
	logins map[int64]string
	// limit replaces Config.RateLimit once a test sets it; negative means spent for everyone.
	limit    int
	limitSet bool
}

type window struct {
	n     int
	reset time.Time
}

func newReadStore(users []User) *readStore {
	r := &readStore{seed: users}
	r.reset()
	return r
}

// reset restores the seeded accounts and clears the rate-limit counters. The caller holds no lock.
func (r *readStore) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts = map[string]*account{}
	r.hits = map[string]*window{}
	r.logins, r.limit, r.limitSet = map[int64]string{}, 0, false
	for _, u := range r.seed {
		a := &account{id: u.ID, login: u.Login, keys: append([]Key(nil), u.Keys...), signing: append([]Key(nil), u.SigningKeys...), repos: map[string]Repo{}}
		for _, repo := range u.Repos {
			a.repos[strings.ToLower(repo.Name)] = repo
		}
		r.accounts[strings.ToLower(u.Login)] = a
	}
}

// limited counts one anonymous call from addr and sets the rate-limit headers; it reports true when
// the budget is spent. A zero limit turns the limit off but the headers stay.
func (s *server) limited(w http.ResponseWriter, r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	s.reads.mu.Lock()
	limit := s.cfg.RateLimit
	if s.reads.limitSet {
		limit = s.reads.limit
	}
	s.reads.mu.Unlock()
	now := s.cfg.Now()
	h := w.Header()
	if limit == 0 { // off: nothing is counted, so nothing is stored per address
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", "5000")
		h.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(rateWindow).Unix(), 10))
		return false
	}
	if limit < 0 { // a test turned the budget into "spent for everyone"
		h.Set("X-RateLimit-Limit", "1")
		h.Set("X-RateLimit-Remaining", "0")
		h.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(rateWindow).Unix(), 10))
		apiError(w, http.StatusForbidden, "API rate limit exceeded")
		return true
	}
	s.reads.mu.Lock()
	win := s.reads.hits[ip]
	if win == nil || !now.Before(win.reset) {
		win = &window{reset: now.Add(rateWindow)}
		s.reads.hits[ip] = win
	}
	exhausted := win.n >= limit
	if !exhausted {
		win.n++
	}
	remaining := max(limit-win.n, 0)
	reset := win.reset
	s.reads.mu.Unlock()
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	if exhausted {
		apiError(w, http.StatusForbidden, "API rate limit exceeded for "+ip)
	}
	return exhausted
}

// read wraps a public read: rate limit first, then the account lookup under the store lock.
func (s *server) read(h func(w http.ResponseWriter, r *http.Request, a *account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.limited(w, r) {
			return
		}
		login := r.PathValue("login")
		if login == "" {
			login = r.PathValue("owner")
		}
		s.reads.mu.Lock()
		defer s.reads.mu.Unlock()
		a := s.reads.accounts[strings.ToLower(login)]
		if a == nil {
			apiError(w, http.StatusNotFound, "Not Found")
			return
		}
		h(w, r, a)
	}
}

func (s *server) userByID(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, u := range s.cfg.Users {
		if err == nil && u.ID == id {
			login := u.Login
			s.reads.mu.Lock()
			if renamed, ok := s.reads.logins[id]; ok {
				login = renamed
			}
			s.reads.mu.Unlock()
			writeJSON(w, map[string]any{"id": u.ID, "login": login, "type": "User"})
			return
		}
	}
	apiError(w, http.StatusNotFound, "Not Found")
}

func keysOrEmpty(k []Key) []Key {
	if k == nil {
		return []Key{}
	}
	return k
}

func (s *server) keys(w http.ResponseWriter, _ *http.Request, a *account) {
	out := make([]map[string]any, 0, len(a.keys))
	for _, k := range a.keys {
		out = append(out, map[string]any{"id": k.ID, "key": k.Key})
	}
	writeJSON(w, out)
}

func (s *server) signingKeys(w http.ResponseWriter, _ *http.Request, a *account) {
	writeJSON(w, keysOrEmpty(a.signing))
}

// publicRepo finds a public repo of the account; a private or missing one is a 404 like GitHub's.
func publicRepo(w http.ResponseWriter, r *http.Request, a *account) (Repo, bool) {
	repo, ok := a.repos[strings.ToLower(r.PathValue("repo"))]
	if !ok || repo.Private {
		apiError(w, http.StatusNotFound, "Not Found")
		return Repo{}, false
	}
	return repo, true
}

func (s *server) repo(w http.ResponseWriter, r *http.Request, a *account) {
	repo, ok := publicRepo(w, r, a)
	if !ok {
		return
	}
	branch := repo.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	owner := r.PathValue("owner")
	writeJSON(w, map[string]any{"name": repo.Name, "full_name": owner + "/" + repo.Name, "private": false,
		"default_branch": branch, "html_url": "https://github.com/" + owner + "/" + repo.Name})
}

func (s *server) rules(w http.ResponseWriter, r *http.Request, a *account) {
	repo, ok := publicRepo(w, r, a)
	if !ok {
		return
	}
	rules := repo.Rules[r.PathValue("branch")]
	if rules == nil {
		rules = []Rule{}
	}
	writeJSON(w, rules)
}

func (s *server) contents(w http.ResponseWriter, r *http.Request, a *account) {
	repo, ok := publicRepo(w, r, a)
	if !ok {
		return
	}
	p := strings.Trim(r.PathValue("path"), "/")
	body, found := repo.Files[p]
	if !found {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	// A git blob id: sha1("blob <size>\x00" + content).
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(body), body))) //nolint:gosec // see import
	name := p[strings.LastIndex(p, "/")+1:]
	writeJSON(w, map[string]any{"type": "file", "name": name, "path": p, "sha": fmt.Sprintf("%x", sum)})
}

// registerReads mounts the unauthenticated reads the CLI makes (design 3.9) and the /_fake/ control routes.
func (s *server) registerReads(mux *http.ServeMux) {
	mux.HandleFunc("GET /user/{id}", s.userByID)
	mux.HandleFunc("GET /users/{login}/keys", s.read(s.keys))
	mux.HandleFunc("GET /users/{login}/ssh_signing_keys", s.read(s.signingKeys))
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.read(s.repo))
	mux.HandleFunc("GET /repos/{owner}/{repo}/rules/branches/{branch...}", s.read(s.rules))
	mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", s.read(s.contents))
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}", s.read(s.commit))
	mux.HandleFunc("GET /repos/{owner}/{repo}/compare/{spec}", s.read(s.compare))
	mux.HandleFunc("PUT /_fake/users/{login}/rename", s.rename)
	mux.HandleFunc("PUT /_fake/rate_limit", s.setRateLimit)

	mux.HandleFunc("PUT /_fake/users/{login}/keys", s.control(func(a *account, _ string, body []byte) error {
		return json.Unmarshal(body, &a.keys)
	}))
	mux.HandleFunc("PUT /_fake/users/{login}/signing_keys", s.control(func(a *account, _ string, body []byte) error {
		return json.Unmarshal(body, &a.signing)
	}))
	mux.HandleFunc("PUT /_fake/users/{login}/repos/{repo}", s.control(func(a *account, name string, body []byte) error {
		var repo Repo
		if err := json.Unmarshal(body, &repo); err != nil {
			return err
		}
		repo.Name = name // the path wins, so the body cannot disagree with it
		a.repos[strings.ToLower(name)] = repo
		return nil
	}))
	mux.HandleFunc("DELETE /_fake/users/{login}/repos/{repo}", func(w http.ResponseWriter, r *http.Request) {
		s.reads.mu.Lock()
		defer s.reads.mu.Unlock()
		if a := s.reads.accounts[strings.ToLower(r.PathValue("login"))]; a != nil {
			delete(a.repos, strings.ToLower(r.PathValue("repo")))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fake/reset", func(w http.ResponseWriter, _ *http.Request) {
		s.reads.reset()
		w.WriteHeader(http.StatusNoContent)
	})
}

// control applies a JSON body to one seeded account. 204 on success, 404 for an unknown login, 400 for bad JSON.
func (s *server) control(apply func(a *account, repo string, body []byte) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			apiError(w, http.StatusBadRequest, "bad JSON")
			return
		}
		s.reads.mu.Lock()
		defer s.reads.mu.Unlock()
		a := s.reads.accounts[strings.ToLower(r.PathValue("login"))]
		if a == nil {
			apiError(w, http.StatusNotFound, "Not Found")
			return
		}
		if err := apply(a, r.PathValue("repo"), raw); err != nil {
			apiError(w, http.StatusBadRequest, "bad JSON")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
