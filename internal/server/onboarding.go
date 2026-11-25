package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	pathCookieName    = "dojo_path"
	pathCookiePath    = "/v1"
	pathCookieTTL     = 24 * time.Hour
	pathCookieVersion = 1
	pathMacLabel      = "dojo-path-v1."
	maxWaitlist       = 3
	maxOnboardingBody = 1 << 10

	pathChoicePath    = "/v1/onboarding/path"
	pathWaitlistPath  = "/v1/onboarding/waitlist"
	pathWaitlistRoute = "DELETE /v1/onboarding/waitlist/{slug}"
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func validSlug(s string) bool { return len(s) <= 64 && slugRE.MatchString(s) }

// pathChoice is the signed content of the dojo_path cookie (design 3.9): slugs only.
type pathChoice struct {
	V        int      `json:"v"`
	Path     string   `json:"path,omitempty"`
	Waitlist []string `json:"waitlist,omitempty"`
	Issued   int64    `json:"issued"`
}

func (s *SignIn) pathMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(pathMacLabel + payload))
	return m.Sum(nil)
}

func (s *SignIn) sealPath(c pathChoice) string {
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(s.pathMAC(p))
}

// readPathCookie returns the verified choice. A cookie that fails the MAC, the version, the shape or
// the 24 hours is ignored: the zero choice and false, never an error.
func (s *SignIn) readPathCookie(r *http.Request) (pathChoice, bool) {
	c, err := r.Cookie(pathCookieName)
	if err != nil {
		return pathChoice{}, false
	}
	p, sig, ok := strings.Cut(c.Value, ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || !hmac.Equal(got, s.pathMAC(p)) {
		return pathChoice{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	var pc pathChoice
	if err != nil || json.Unmarshal(raw, &pc) != nil || pc.V != pathCookieVersion || len(pc.Waitlist) > maxWaitlist {
		return pathChoice{}, false
	}
	if (pc.Path != "" && !validSlug(pc.Path)) || slices.ContainsFunc(pc.Waitlist, func(w string) bool { return !validSlug(w) }) {
		return pathChoice{}, false
	}
	age := s.now().Sub(time.Unix(pc.Issued, 0))
	if age > pathCookieTTL || age < -stateSkew {
		return pathChoice{}, false
	}
	return pc, true
}

func (s *SignIn) pathCookie(v string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows PUBLIC_ORIGIN by design (3.1)
		Name: pathCookieName, Value: v, Path: pathCookiePath, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Auth.Secure,
	}
}

// writePathCookie stores the choice for another 24 hours, or clears the cookie when it holds nothing.
func (s *SignIn) writePathCookie(w http.ResponseWriter, c pathChoice) {
	if c.Path == "" && len(c.Waitlist) == 0 {
		http.SetCookie(w, s.pathCookie("", -1))
		return
	}
	c.V, c.Issued = pathCookieVersion, s.now().Unix()
	http.SetCookie(w, s.pathCookie(s.sealPath(c), int(pathCookieTTL.Seconds())))
}

// applyChoice writes the choice carried by the sealed state onto the learner: the path only while
// the learner has none, the waitlist rows for planned paths (design 3.9).
func (s *SignIn) applyChoice(ctx context.Context, l repo.Learner, st oauthState) error {
	if st.Path == "" && len(st.Waitlist) == 0 {
		return nil
	}
	return s.Store.ApplyLearnerChoice(ctx, repo.ApplyLearnerChoiceParams{OrgID: l.OrgID, LearnerID: l.ID,
		Path: pgtype.Text{String: st.Path, Valid: st.Path != ""}, Waitlist: st.Waitlist})
}

// choiceGuard refuses a request that is not JSON with the CSRF header. The middleware already did so for
// a cookie principal; the onboarding writes ask it of every caller, signed out and bearer too.
func choiceGuard(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" || r.Header.Get(CSRFHeader) == "" {
		writeError(w, http.StatusForbidden, "csrf_rejected")
		return false
	}
	return true
}

// choiceCaller runs the checks every onboarding write shares and reports whether the caller is a learner.
func (s *SignIn) choiceCaller(w http.ResponseWriter, r *http.Request) (p principal, signedIn, ok bool) {
	if !choiceGuard(w, r) {
		return p, false, false
	}
	p, signedIn = principalOf(r.Context())
	if signedIn && p.suspended {
		writeError(w, http.StatusForbidden, "account_suspended")
		return p, true, false
	}
	return p, signedIn, true
}

type pathBody struct {
	Path string `json:"path"`
}

// choiceSlug reads {"path": "<slug>"} and finds the path.
func (s *SignIn) choiceSlug(w http.ResponseWriter, r *http.Request) (repo.GetPathBySlugRow, bool) {
	var b pathBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOnboardingBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || dec.Decode(&struct{}{}) != io.EOF || !validSlug(b.Path) {
		writeError(w, http.StatusBadRequest, "bad_request")
		return repo.GetPathBySlugRow{}, false
	}
	return s.findPath(w, r, b.Path)
}

func (s *SignIn) findPath(w http.ResponseWriter, r *http.Request, slug string) (repo.GetPathBySlugRow, bool) {
	p, err := s.Store.GetPathBySlug(r.Context(), slug)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found")
	case err != nil:
		choiceFailed(w, r, err)
	default:
		return p, true
	}
	return p, false
}

func choiceFailed(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	slog.Error("onboarding write failed", "path", r.Pattern, "error", err)
	writeError(w, http.StatusInternalServerError, "server_error")
}

// postPath is POST /v1/onboarding/path: the cookie when signed out, chosen_path_id when signed in.
func (s *SignIn) postPath(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pr, signedIn, ok := s.choiceCaller(w, r)
	if !ok {
		return
	}
	p, ok := s.choiceSlug(w, r)
	if !ok {
		return
	}
	if p.Status != "available" {
		writeError(w, http.StatusConflict, "path_planned")
		return
	}
	if signedIn {
		if err := s.Store.SetLearnerChosenPath(r.Context(), repo.SetLearnerChosenPathParams{ID: pr.learner, ChosenPathID: p.ID}); err != nil {
			choiceFailed(w, r, err)
			return
		}
	} else {
		c, _ := s.readPathCookie(r)
		c.Path = p.Slug
		s.writePathCookie(w, c)
	}
	w.WriteHeader(http.StatusNoContent)
}

// postWaitlist is POST /v1/onboarding/waitlist: "Tell me when" for a planned path.
func (s *SignIn) postWaitlist(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pr, signedIn, ok := s.choiceCaller(w, r)
	if !ok {
		return
	}
	p, ok := s.choiceSlug(w, r)
	if !ok {
		return
	}
	if p.Status != "planned" {
		writeError(w, http.StatusConflict, "path_available")
		return
	}
	if signedIn {
		if err := s.Store.InsertPathWaitlist(r.Context(), repo.InsertPathWaitlistParams{ID: pr.learner, PathID: p.ID}); err != nil {
			choiceFailed(w, r, err)
			return
		}
	} else {
		c, _ := s.readPathCookie(r)
		if !slices.Contains(c.Waitlist, p.Slug) {
			if len(c.Waitlist) >= maxWaitlist {
				writeError(w, http.StatusBadRequest, "bad_request")
				return
			}
			c.Waitlist = append(c.Waitlist, p.Slug)
		}
		s.writePathCookie(w, c)
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteWaitlist is DELETE /v1/onboarding/waitlist/{slug}.
func (s *SignIn) deleteWaitlist(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pr, signedIn, ok := s.choiceCaller(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	if !validSlug(slug) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	p, ok := s.findPath(w, r, slug)
	if !ok {
		return
	}
	if p.Status != "planned" {
		writeError(w, http.StatusConflict, "path_available")
		return
	}
	if signedIn {
		if err := s.Store.DeletePathWaitlist(r.Context(), repo.DeletePathWaitlistParams{LearnerID: pr.learner, PathID: p.ID}); err != nil {
			choiceFailed(w, r, err)
			return
		}
	} else if c, ok := s.readPathCookie(r); ok {
		c.Waitlist = slices.DeleteFunc(slices.Clone(c.Waitlist), func(w string) bool { return w == p.Slug })
		s.writePathCookie(w, c)
	}
	w.WriteHeader(http.StatusNoContent)
}

// isOnboardingWrite reports the routed patterns of the onboarding writes: per client IP signed out,
// the shared learner write budget signed in.
func isOnboardingWrite(pattern string) bool {
	return pattern == "POST "+pathChoicePath || pattern == "POST "+pathWaitlistPath || pattern == pathWaitlistRoute
}
