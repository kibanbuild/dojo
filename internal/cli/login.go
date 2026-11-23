package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kibanbuild/dojo/internal/state"
)

// Version is the CLI build version; releases set it with -ldflags "-X .../internal/cli.Version=v1.2.3".
var Version = "dev"

// Production defaults, set at build time with -ldflags "-X .../internal/cli.DefaultAPIURL=..." and
// friends. They stay empty until the domain exists (design 3.1, ledger): an empty one makes
// dojo login ask for DOJO_API_URL instead of guessing a host. GitHub's own host is not ours to decide.
var (
	DefaultAPIURL      = ""
	DefaultGitHubURL   = "https://github.com"
	DefaultGitHubOAuth = "" // our OAuth App client id
)

// LoginConfig says where login and logout talk to; each empty field falls back to the build default.
type LoginConfig struct {
	APIURL      string // DOJO_API_URL
	GitHubURL   string // DOJO_GITHUB_URL
	GitHubOAuth string // DOJO_GITHUB_CLIENT_ID
}

// LoginConfigFromEnv reads the DOJO_* variables.
func LoginConfigFromEnv() LoginConfig {
	return LoginConfig{APIURL: os.Getenv("DOJO_API_URL"), GitHubURL: os.Getenv("DOJO_GITHUB_URL"),
		GitHubOAuth: os.Getenv("DOJO_GITHUB_CLIENT_ID")}
}

const (
	deviceScope   = "read:user user:email"
	deviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	maxReply      = 1 << 20
	maxPollErrors = 4 // consecutive transport failures while polling GitHub before giving up
	minInterval   = time.Second
)

func (a *App) httpClient() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (a *App) sleep(ctx context.Context, d time.Duration) error {
	if a.Sleep != nil {
		return a.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// resolve applies flag over config over build default and checks the URL.
func resolveURL(name, flagVal, cfgVal, def, envName string, required bool) (string, error) {
	v := flagVal
	if v == "" {
		v = cfgVal
	}
	if v == "" {
		v = def
	}
	if v == "" {
		if required {
			return "", fmt.Errorf("no %s is configured: set %s (local stack: see the README)", name, envName)
		}
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s %q is not an http(s) URL", name, v)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("%s %q must use https (plain http is only for localhost)", name, v)
	}
	return strings.TrimRight(v, "/"), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// printable drops control and format characters from text GitHub sends us before it reaches the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

type loginFlags struct {
	api, github, client string
	force, local        bool
}

func newLoginFlags(cmd string, fset *flag.FlagSet) *loginFlags {
	f := &loginFlags{}
	fset.StringVar(&f.api, "api-url", "", "API base URL (env DOJO_API_URL)")
	switch cmd {
	case "login":
		fset.StringVar(&f.github, "github-url", "", "GitHub base URL (env DOJO_GITHUB_URL)")
		fset.StringVar(&f.client, "client-id", "", "GitHub OAuth client id (env DOJO_GITHUB_CLIENT_ID)")
		fset.BoolVar(&f.force, "force", false, "log in again even if this computer is already logged in")
	case "logout":
		fset.BoolVar(&f.local, "local", false, "only forget the token on this computer; do not tell the API")
	}
	return f
}

// apiReply is one answer from the API: the status, the error code of an {"error": ...} body and Retry-After.
type apiReply struct {
	status     int
	body       []byte
	code       string
	retryAfter time.Duration
}

func (a *App) callAPI(ctx context.Context, method, base, path, bearer string, body any) (*apiReply, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, err
	}
	r := &apiReply{status: resp.StatusCode, body: raw}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		r.code = e.Error
	}
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		r.retryAfter = time.Duration(s) * time.Second
	}
	return r, nil
}

// netErr hides the request URL's query and any detail beyond what the learner can act on.
func netErr(what string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// whoAmI asks the API who the token belongs to.
func (a *App) whoAmI(ctx context.Context, api, token string) (string, *apiReply, error) {
	r, err := a.callAPI(ctx, http.MethodGet, api, "/v1/me", token, nil)
	if err != nil {
		return "", nil, err
	}
	if r.status != http.StatusOK {
		return "", r, nil
	}
	var me struct {
		Username    string `json:"username"`
		GithubLogin string `json:"github_login"`
	}
	if err := json.Unmarshal(r.body, &me); err != nil {
		return "", r, fmt.Errorf("the API answered /v1/me with something unexpected: %w", err)
	}
	if me.Username == "" {
		me.Username = me.GithubLogin
	}
	return me.Username, r, nil
}

func (a *App) login(ctx context.Context, f *loginFlags) error {
	api, err := resolveURL("API URL", f.api, a.Login.APIURL, DefaultAPIURL, "DOJO_API_URL", true)
	if err != nil {
		return err
	}
	gh, err := resolveURL("GitHub URL", f.github, a.Login.GitHubURL, DefaultGitHubURL, "DOJO_GITHUB_URL", true)
	if err != nil {
		return err
	}
	clientID := firstNonEmpty(f.client, a.Login.GitHubOAuth, DefaultGitHubOAuth)
	if clientID == "" {
		return errors.New("no GitHub client id is configured: set DOJO_GITHUB_CLIENT_ID (local stack: see the README)")
	}

	if !f.force {
		if done, err := a.alreadyLoggedIn(ctx, api); done || err != nil {
			return err
		}
	}

	ghToken, err := a.deviceFlow(ctx, gh, clientID)
	if err != nil {
		return err
	}
	r, err := a.callAPI(ctx, http.MethodPost, api, "/v1/auth/cli", "", map[string]string{
		"access_token": ghToken, "device_name": deviceName(), "os": runtime.GOOS + "/" + runtime.GOARCH,
		"cli_version": cleanVersion(Version)})
	if err != nil {
		return netErr("could not reach the Dojo API", err)
	}
	if r.status != http.StatusCreated {
		return a.loginError(r, api)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || out.Token == "" {
		return errors.New("the API accepted the login but sent no token; run dojo login again")
	}
	if err := state.SaveToken(out.Token); err != nil {
		return fmt.Errorf("logged in, but the token could not be saved: %w (fix DOJO_HOME and run dojo login again)", err)
	}
	name, meReply, err := a.whoAmI(ctx, api, out.Token)
	a.alignUsageStats(meReply)
	if err != nil || name == "" {
		fmt.Fprintln(a.Out, "Logged in.")
		return nil
	}
	fmt.Fprintf(a.Out, "Logged in as %s\n", name)
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// alreadyLoggedIn reports true when a stored token still works, after saying who it is.
func (a *App) alreadyLoggedIn(ctx context.Context, api string) (bool, error) {
	tok, err := state.LoadToken()
	if errors.Is(err, state.ErrNoToken) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not read the saved token: %w (dojo login --force replaces it)", err)
	}
	name, r, err := a.whoAmI(ctx, api, tok)
	switch {
	case err != nil:
		return false, netErr("could not check the saved login (dojo login --force logs in again anyway)", err)
	case name != "":
		a.alignUsageStats(r)
		fmt.Fprintf(a.Out, "Already logged in as %s. Run dojo logout first to switch accounts.\n", name)
		return true, nil
	case r.status == http.StatusForbidden && r.code == "account_suspended":
		return false, errors.New("this account is suspended; contact support")
	case r.status == http.StatusUnauthorized:
		return false, nil // revoked or stale: log in again
	default:
		return false, fmt.Errorf("could not check the saved login: the API answered %d (dojo login --force logs in again anyway)", r.status)
	}
}

func (a *App) loginError(r *apiReply, api string) error {
	switch {
	case r.status == http.StatusForbidden && r.code == "signup_required":
		return fmt.Errorf("no Dojo account for this GitHub user yet: sign up at %s/app first, then run dojo login again", api)
	case r.status == http.StatusForbidden && r.code == "account_suspended":
		return errors.New("this account is suspended; contact support")
	case r.status == http.StatusUnauthorized && r.code == "github_rejected":
		return errors.New("GitHub did not accept the sign-in (it may have expired); run dojo login again")
	case r.status == http.StatusTooManyRequests:
		if r.retryAfter > 0 {
			return fmt.Errorf("too many login attempts: wait %d seconds and run dojo login again", int(r.retryAfter.Seconds()))
		}
		return errors.New("too many login attempts: wait a minute and run dojo login again")
	case r.status == http.StatusBadGateway && r.code == "github_unavailable":
		return errors.New("the API could not reach GitHub; try dojo login again in a moment")
	case r.status == http.StatusBadRequest:
		return errors.New("the API refused the login request (is this CLI up to date?); update dojo and try again")
	default:
		return fmt.Errorf("the API failed the login (%d); try again in a moment", r.status)
	}
}

// deviceFlow runs GitHub's device flow and returns the access token, which only lives in memory.
func (a *App) deviceFlow(ctx context.Context, gh, clientID string) (string, error) {
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	if _, err := a.postForm(ctx, gh+"/login/device/code", url.Values{"client_id": {clientID}, "scope": {deviceScope}}, &dc); err != nil {
		return "", netErr("could not reach GitHub", err)
	}
	if dc.Error != "" || dc.DeviceCode == "" || dc.UserCode == "" || dc.VerificationURI == "" {
		return "", fmt.Errorf("GitHub refused to start the sign-in (%s); check DOJO_GITHUB_CLIENT_ID", firstNonEmpty(dc.Error, "unexpected answer"))
	}
	if dc.ExpiresIn <= 0 {
		dc.ExpiresIn = 900
	}
	interval := max(time.Duration(dc.Interval)*time.Second, minInterval)
	deadline := a.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	fmt.Fprintf(a.Out, "To sign in, open %s and enter the code:\n\n    %s\n\nWaiting for you to approve in the browser (Ctrl-C to cancel)...\n", printable(dc.VerificationURI), printable(dc.UserCode))

	failures := 0
	for {
		if err := a.sleep(ctx, interval); err != nil {
			return "", errors.New("login cancelled")
		}
		if !a.Now().Before(deadline) {
			return "", errors.New("the code expired before it was approved; run dojo login again")
		}
		var tr struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		_, err := a.postForm(ctx, gh+"/login/oauth/access_token", url.Values{"client_id": {clientID},
			"device_code": {dc.DeviceCode}, "grant_type": {deviceGrant}}, &tr)
		if err != nil {
			if ctx.Err() != nil {
				return "", errors.New("login cancelled")
			}
			if failures++; failures >= maxPollErrors {
				return "", netErr("lost contact with GitHub", err)
			}
			continue
		}
		failures = 0
		switch {
		case tr.AccessToken != "":
			return tr.AccessToken, nil
		case tr.Error == "authorization_pending":
		case tr.Error == "slow_down":
			if tr.Interval > 0 {
				interval = max(time.Duration(tr.Interval)*time.Second, interval)
			} else {
				interval += 5 * time.Second
			}
		case tr.Error == "expired_token" || tr.Error == "token_expired":
			return "", errors.New("the code expired before it was approved; run dojo login again")
		case tr.Error == "access_denied":
			return "", errors.New("the sign-in was cancelled on GitHub; run dojo login to try again")
		default:
			return "", fmt.Errorf("GitHub stopped the sign-in (%s); run dojo login again", firstNonEmpty(tr.Error, "unexpected answer"))
		}
	}
}

// postForm posts a form and decodes GitHub's JSON answer (any status) into out.
func (a *App) postForm(ctx context.Context, u string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("unexpected answer (status %d)", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// deviceName is the hostname cut down to what the API accepts: 1 to 64 characters, no control or
// invisible characters, no leading or trailing space.
func deviceName() string {
	h, _ := os.Hostname()
	return sanitizeDeviceName(h)
}

func sanitizeDeviceName(h string) string {
	h = strings.ToValidUTF8(h, "")
	h = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, h)
	if utf8.RuneCountInString(h) > 64 {
		h = string([]rune(h)[:64])
	}
	h = strings.TrimSpace(h)
	if h == "" {
		return "unknown"
	}
	return h
}

// cleanVersion keeps a build version inside the API's 32 characters of A-Za-z0-9._+-.
func cleanVersion(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._+-", r)) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		return "dev"
	}
	return s
}

func (a *App) logout(ctx context.Context, f *loginFlags) error {
	tok, err := state.LoadToken()
	if errors.Is(err, state.ErrNoToken) {
		fmt.Fprintln(a.Out, "Not logged in.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not read the saved token: %w", err)
	}
	forget := func(msg string) error {
		if err := state.RemoveToken(); err != nil {
			return fmt.Errorf("could not remove the saved token: %w (delete it by hand)", err)
		}
		fmt.Fprintln(a.Out, msg)
		return nil
	}
	if f.local {
		return forget("Logged out on this computer only. The token still works elsewhere until you revoke it on the web.")
	}
	api, err := resolveURL("API URL", f.api, a.Login.APIURL, DefaultAPIURL, "DOJO_API_URL", true)
	if err != nil {
		return err
	}
	r, err := a.callAPI(ctx, http.MethodDelete, api, "/v1/auth/cli", tok, nil)
	if err != nil {
		return fmt.Errorf("%w: you are still logged in (dojo logout --local forgets the token on this computer only)",
			netErr("could not reach the Dojo API", err))
	}
	switch r.status {
	case http.StatusNoContent, http.StatusUnauthorized: // revoked now, or already dead
		return forget("Logged out.")
	default:
		return fmt.Errorf("the API answered %d: you are still logged in (try again, or dojo logout --local)", r.status)
	}
}
