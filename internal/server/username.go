package server

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	usernameTries = 5
	usernameMax   = 30
)

var usernameRE = regexp.MustCompile(`^[a-z0-9-]{3,30}$`)

// reservedUsernames are names a learner must not take: the site's routes (design 2.2), the operator
// surfaces, and names that read as staff or the product. None has a hyphen, so a fallback never collides.
var reservedUsernames = map[string]bool{
	"admin": true, "administrator": true, "operator": true, "operators": true, "root": true, "system": true,
	"staff": true, "support": true, "help": true, "security": true, "abuse": true, "moderator": true,
	"api": true, "app": true, "start": true, "review": true, "story": true, "pricing": true, "privacy": true,
	"terms": true, "conduct": true, "settings": true, "login": true, "logout": true, "signin": true,
	"signup": true, "auth": true, "www": true, "mail": true, "billing": true, "status": true, "docs": true,
	"blog": true, "dojo": true, "kiban": true, "kibanbuild": true, "kibanya": true, "kibanyadev": true, "null": true, "undefined": true,
}

// defaultUsername is the lowercased GitHub login when it is valid and not reserved.
func defaultUsername(login string) (string, bool) {
	n := strings.ToLower(login)
	return n, usernameRE.MatchString(n) && !reservedUsernames[n]
}

// fallbackUsername always fits the CHECK: the sanitized login, cut so the whole name stays within
// usernameMax, plus "-" and the suffix. The suffix is at most 15 characters, so a base of 14 survives.
func fallbackUsername(login, suffix string) string {
	base := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, strings.ToLower(login))
	if max := usernameMax - 1 - len(suffix); len(base) > max {
		base = base[:max]
	}
	if base = strings.Trim(base, "-"); base == "" {
		base = "learner"
	}
	return base + "-" + suffix
}

// usernameSuffix is stable for one GitHub account: its id in base 36 (at most 13 characters), and from
// the second try on "-<try>" after it. Two accounts never share the id part.
func usernameSuffix(githubID int64, try int) string {
	s := strconv.FormatInt(githubID, 36)
	if try > 1 {
		s += "-" + strconv.Itoa(try)
	}
	return s
}

// createWithUsername is deterministic: the lowercased login when it fits and is not reserved, else
// "<base>-<id36>", and when a name is taken (a unique violation, the only error retried) the same
// with "-2", "-3"... after the id. The same input always gives the same sequence of names.
func createWithUsername(login string, githubID int64, insert func(name string) (repo.Learner, error)) (repo.Learner, error) {
	for try := 0; try < usernameTries; try++ {
		name, ok := defaultUsername(login)
		if !ok {
			name = fallbackUsername(login, usernameSuffix(githubID, try+1))
		} else if try > 0 {
			name = fallbackUsername(login, usernameSuffix(githubID, try))
		}
		l, err := insert(name)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			continue
		}
		return l, err
	}
	return repo.Learner{}, errors.New("no free username")
}
