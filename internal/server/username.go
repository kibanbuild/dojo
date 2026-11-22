package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	usernameTries = 5
	// baseMax leaves room for "-" and a 6-character suffix inside the 30-character limit.
	baseMax = 23
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
	"blog": true, "dojo": true, "kiban": true, "kibanbuild": true, "null": true, "undefined": true,
}

// defaultUsername is the lowercased GitHub login when it is valid and not reserved.
func defaultUsername(login string) (string, bool) {
	n := strings.ToLower(login)
	return n, usernameRE.MatchString(n) && !reservedUsernames[n]
}

// fallbackUsername always fits the CHECK: the sanitized login, cut to baseMax, plus "-" and the suffix.
func fallbackUsername(login, suffix string) string {
	base := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, strings.ToLower(login))
	if len(base) > baseMax {
		base = base[:baseMax]
	}
	if base = strings.Trim(base, "-"); base == "" {
		base = "learner"
	}
	return base + "-" + suffix
}

func randSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createWithUsername tries the default name, then random suffixes, retrying only a unique violation.
func createWithUsername(login string, insert func(name string) (repo.Learner, error)) (repo.Learner, error) {
	for i := 0; i < usernameTries; i++ {
		name, ok := defaultUsername(login)
		if i > 0 || !ok {
			name = fallbackUsername(login, randSuffix())
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
