package doctor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kibanyadev/dojo/internal/ghread"
)

// GitHubReader is the part of ghread.Client the GitHub checks use.
type GitHubReader interface {
	Keys(ctx context.Context, login string) ([]ghread.Key, error)
	SigningKeys(ctx context.Context, login string) ([]ghread.Key, error)
	Repo(ctx context.Context, owner, repo string) (ghread.Repo, error)
	BranchRules(ctx context.Context, owner, repo, branch string) ([]ghread.Rule, error)
	Contents(ctx context.Context, owner, repo, path string) (ghread.Content, error)
}

// Account is the learner's GitHub account, as GET /v1/me reports it.
type Account struct {
	Login string
	ID    int64
}

const (
	newKeyURL  = "https://github.com/settings/ssh/new"
	newRepoURL = "https://github.com/new"
)

var requiredRules = []struct{ typ, label string }{
	{"required_signatures", "Require signed commits"},
	{"non_fast_forward", "Block force pushes"},
	{"deletion", "Restrict deletions"},
}

// EvaluateGitHub runs the checks that need GitHub: at most five reads, none once one of them could
// not be judged (the rest report the same reason), and key_auth and the repo checks skip a read
// they do not need. An undone step is a fail; anything GitHub did not answer is unknown.
func EvaluateGitHub(ctx context.Context, m Machine, who Account, gh GitHubReader) []Result {
	var rs []Result
	var stopped error
	// call makes one read unless an earlier one could not be judged.
	call := func(f func() error) error {
		if stopped != nil {
			return stopped
		}
		err := f()
		if errors.Is(err, ghread.ErrUnknown) || errors.Is(err, ghread.ErrInvalid) {
			stopped = err
		}
		return err
	}
	add := func(id string, g Group, s Status, text, fix string) { rs = append(rs, res(id, g, s, text, fix)) }
	unknown := func(id string, g Group, err error, what string) {
		text := strings.TrimPrefix(err.Error(), "github: ")
		switch {
		case errors.Is(err, ghread.ErrNotFound):
			text = "GitHub could not find " + what
		case errors.Is(err, ghread.ErrInvalid):
			text = "the GitHub login Dojo has for you is not valid, so " + what + " was not checked"
		}
		add(id, g, Unknown, text, "run dojo doctor again in a moment")
	}
	account, rulesOf := "the account "+who.Login, "the rules of main in "+who.Login+"/dojo"

	add("git_email_github", GroupAdvice, emailStatus(m.GitEmail, who), "git email is "+emailWord(m.GitEmail, who), emailFix(m.GitEmail, who))

	signing := keyFields(m.SigningPub)
	switch {
	case signing == "" && m.GitSigningKey != "":
		add("key_signing", GroupKey, Fail, "git's signing key cannot be read (user.signingkey)",
			"point user.signingkey at your public key file: git config --global user.signingkey ~/.ssh/id_ed25519.pub")
	case signing == "":
		add("key_signing", GroupKey, Fail, "no SSH key to look for among your GitHub signing keys",
			"create an SSH key first (ssh-keygen -t ed25519), then add it on GitHub as a signing key: "+newKeyURL)
	default:
		var keys []ghread.Key
		err := call(func() (err error) { keys, err = gh.SigningKeys(ctx, who.Login); return })
		switch {
		case err != nil:
			unknown("key_signing", GroupKey, err, account)
		case hasKey(keys, signing):
			add("key_signing", GroupKey, OK, "GitHub lists your signing key", "")
		default:
			add("key_signing", GroupKey, Fail, "GitHub does not list the key git signs with as a signing key",
				"add the public key git signs with at "+newKeyURL+", Key type: Signing Key")
		}
	}

	switch offered := keyFieldsAll(m.SSHOfferedKeys); {
	case !m.CloneOverSSH:
		add("key_auth", GroupKey, OK, "Not needed: your clone uses HTTPS", "")
	case len(offered) == 0:
		add("key_auth", GroupKey, Fail, "ssh offers no key for github.com",
			"create an SSH key, or point github.com at one in ~/.ssh/config, then add it at "+newKeyURL+", Key type: Authentication Key")
	default:
		var keys []ghread.Key
		err := call(func() (err error) { keys, err = gh.Keys(ctx, who.Login); return })
		switch {
		case err != nil:
			unknown("key_auth", GroupKey, err, account)
		case slices.ContainsFunc(offered, func(k string) bool { return hasKey(keys, k) }):
			add("key_auth", GroupKey, OK, "GitHub lists a key that ssh offers for github.com", "")
		default:
			add("key_auth", GroupKey, Fail, "GitHub does not list a key that ssh offers for github.com as an authentication key",
				"add it at "+newKeyURL+", Key type: Authentication Key (GitHub keeps signing and authentication keys apart, so one key is added twice)")
		}
	}

	full := who.Login + "/dojo"
	createRepo := "create it, public: " + newRepoURL + ", name it dojo, visibility Public"
	var repo ghread.Repo
	repoErr := call(func() (err error) { repo, err = gh.Repo(ctx, who.Login, "dojo"); return })
	repoOK := false
	switch {
	case errors.Is(repoErr, ghread.ErrNotFound):
		add("repo", GroupRepo, Fail, "no public repo "+full, createRepo)
	case repoErr != nil:
		unknown("repo", GroupRepo, repoErr, "the repo "+full)
	case repo.Private:
		add("repo", GroupRepo, Fail, "the repo "+full+" is private", createRepo)
	default:
		repoOK = true
		add("repo", GroupRepo, OK, "public repo "+full+" found", "")
	}

	needRepo := "no public repo to look at yet"
	if !repoOK && repoErr != nil && !errors.Is(repoErr, ghread.ErrNotFound) {
		unknown("ruleset", GroupRepo, repoErr, rulesOf)
		unknown("agents_md", GroupRepo, repoErr, "AGENTS.md")
		return rs
	}
	if !repoOK {
		add("ruleset", GroupRepo, Fail, needRepo, createRepo)
		add("agents_md", GroupRepo, Fail, needRepo, createRepo)
		return rs
	}

	var rules []ghread.Rule
	err := call(func() (err error) { rules, err = gh.BranchRules(ctx, who.Login, "dojo", "main"); return })
	if err != nil {
		unknown("ruleset", GroupRepo, err, rulesOf)
	} else {
		var missing, labels []string
		for _, r := range requiredRules {
			labels = append(labels, r.label)
			if !slices.ContainsFunc(rules, func(x ghread.Rule) bool { return x.Type == r.typ }) {
				missing = append(missing, r.typ)
			}
		}
		if len(missing) == 0 {
			add("ruleset", GroupRepo, OK, "main is protected: signed commits, no force push, no deletion", "")
		} else {
			add("ruleset", GroupRepo, Fail, "main lacks: "+strings.Join(missing, ", "),
				fmt.Sprintf("on %s: Settings > Rules > Rulesets > New branch ruleset, target main, enforcement Active, turn on %s", full, strings.Join(labels, ", ")))
		}
	}

	var file ghread.Content
	err = call(func() (err error) { file, err = gh.Contents(ctx, who.Login, "dojo", "AGENTS.md"); return })
	switch {
	case errors.Is(err, ghread.ErrNotFound) || (err == nil && file.Type != "file"):
		add("agents_md", GroupRepo, Fail, "no AGENTS.md in "+full,
			"copy the AGENTS.md text from the Dojo page into the root of the repo and commit it")
	case err != nil:
		unknown("agents_md", GroupRepo, err, "AGENTS.md")
	default:
		add("agents_md", GroupRepo, OK, "AGENTS.md is in "+full, "")
	}
	return rs
}

func noreply(who Account) string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", who.ID, who.Login)
}

func isNoreply(email string, who Account) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	return e == strings.ToLower(noreply(who)) || e == strings.ToLower(who.Login)+"@users.noreply.github.com"
}

func emailStatus(email string, who Account) Status {
	if isNoreply(email, who) {
		return OK
	}
	return Warn
}

func emailWord(email string, who Account) string {
	if isNoreply(email, who) {
		return "your GitHub noreply address"
	}
	return "not your GitHub noreply address"
}

func emailFix(email string, who Account) string {
	if isNoreply(email, who) {
		return ""
	}
	return fmt.Sprintf("optional: git config --global user.email %q, so GitHub shows your commits as Verified", noreply(who))
}

// keyFields reduces a public key line to "type base64", the part that identifies it.
func keyFields(s string) string {
	f := strings.Fields(s)
	if len(f) < 2 {
		return ""
	}
	return f[0] + " " + f[1]
}

func keyFieldsAll(keys []string) []string {
	var out []string
	for _, k := range keys {
		if f := keyFields(k); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func hasKey(keys []ghread.Key, want string) bool {
	return slices.ContainsFunc(keys, func(k ghread.Key) bool { return keyFields(k.Key) == want })
}
