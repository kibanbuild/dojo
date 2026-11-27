package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/github"
	"github.com/kibanyadev/dojo/internal/server/repo"
)

type fakeCommitStore struct {
	leases   []repo.CommitLease
	finished []repo.CommitResult
	logins   []string
}

func (f *fakeCommitStore) LeaseCommits(context.Context, int) ([]repo.CommitLease, error) {
	return f.leases, nil
}
func (f *fakeCommitStore) FinishCommit(_ context.Context, r repo.CommitResult) (bool, error) {
	f.finished = append(f.finished, r)
	return true, nil
}
func (f *fakeCommitStore) SetGithubLogin(_ context.Context, _ pgtype.UUID, login string) error {
	f.logins = append(f.logins, login)
	return nil
}

type fakeCommitReader struct {
	commit    map[string]github.CommitInfo // by login
	commitErr map[string]error
	compare   github.Comparison
	cmpErr    error
	user      github.User
	userErr   error
	calls     []string
}

func (f *fakeCommitReader) UserByID(context.Context, int64) (github.User, error) {
	f.calls = append(f.calls, "user")
	return f.user, f.userErr
}
func (f *fakeCommitReader) Commit(_ context.Context, login, _ string) (github.CommitInfo, error) {
	f.calls = append(f.calls, "commit:"+login)
	return f.commit[login], f.commitErr[login]
}
func (f *fakeCommitReader) Compare(_ context.Context, login, _, _ string) (github.Comparison, error) {
	f.calls = append(f.calls, "compare:"+login)
	return f.compare, f.cmpErr
}

const itemID = "support-l1/perms-03"

func lease(tries int32, expired bool) repo.CommitLease {
	return repo.CommitLease{ItemID: itemID, Commit: "c", Tries: tries, Expired: expired, GithubID: 7, GithubLogin: "ada"}
}

func goodCommit() github.CommitInfo {
	return github.CommitInfo{Verified: true, Message: "pass: " + itemID + "\n\nDojo-Ticket: " + itemID + "\nDojo-Event: x\n"}
}

func TestVerifierOutcomes(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name      string
		lease     repo.CommitLease
		r         *fakeCommitReader
		wantState string // "" = no write
		wantTries int32
		wantDelay time.Duration
	}{
		{"verified", lease(2, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, compare: github.Comparison{Status: "behind"}}, "verified", 2, 0},
		{"identical is on main", lease(0, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, compare: github.Comparison{Status: "identical"}}, "verified", 0, 0},
		{"ahead is not on main", lease(0, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, compare: github.Comparison{Status: "ahead"}}, "pending", 1, 5 * time.Second},
		{"diverged", lease(1, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, compare: github.Comparison{Status: "diverged"}}, "pending", 2, time.Minute},
		{"signature not verified", lease(2, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": {Verified: false, Message: goodCommit().Message}}}, "pending", 3, 5 * time.Minute},
		{"other ticket in message", lease(5, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": {Verified: true, Message: "Dojo-Ticket: other/x\n"}}}, "pending", 6, 6 * time.Hour},
		{"seventh try waits a day", lease(6, false), &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrNotFound}, user: github.User{Login: "ada"}}, "pending", 7, 24 * time.Hour},
		{"404 with the same login counts", lease(0, false), &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrNotFound}, user: github.User{Login: "ada"}}, "pending", 1, 5 * time.Second},
		{"rate limit is not a try", lease(0, false), &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrRateLimited}}, "", 0, 0},
		{"5xx is not a try", lease(0, false), &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrUnavailable}}, "", 0, 0},
		{"compare 5xx is not a try", lease(0, false), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, cmpErr: boom}, "", 0, 0},
		{"refresh failure is not a try", lease(0, false), &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrNotFound}, userErr: github.ErrUnavailable}, "", 0, 0},
		{"7 days: failed signature", lease(9, true), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": {Verified: false}}}, "unverified", 9, 0},
		{"7 days: github error", lease(9, true), &fakeCommitReader{commitErr: map[string]error{"ada": boom}}, "unverified", 9, 0},
		{"7 days: still verified wins", lease(9, true), &fakeCommitReader{commit: map[string]github.CommitInfo{"ada": goodCommit()}, compare: github.Comparison{Status: "behind"}}, "verified", 9, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeCommitStore{leases: []repo.CommitLease{tc.lease}}
			v := &CommitVerifier{Store: s, GitHub: tc.r, Batch: 20}
			if _, err := v.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.wantState == "" {
				if len(s.finished) != 0 {
					t.Fatalf("wrote %+v", s.finished)
				}
				return
			}
			if len(s.finished) != 1 {
				t.Fatalf("writes: %+v", s.finished)
			}
			got := s.finished[0]
			if got.State != tc.wantState || got.Tries != tc.wantTries || got.DelaySeconds != tc.wantDelay.Seconds() {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestVerifierRefreshesRenamedLoginOnceAndRetries(t *testing.T) {
	r := &fakeCommitReader{
		commit:    map[string]github.CommitInfo{"ada2": goodCommit()},
		commitErr: map[string]error{"ada": github.ErrNotFound},
		compare:   github.Comparison{Status: "behind"},
		user:      github.User{Login: "ada2"},
	}
	s := &fakeCommitStore{leases: []repo.CommitLease{lease(0, false)}}
	if _, err := (&CommitVerifier{Store: s, GitHub: r, Batch: 1}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.logins) != 1 || s.logins[0] != "ada2" || len(s.finished) != 1 || s.finished[0].State != "verified" {
		t.Fatalf("logins %v finished %+v", s.logins, s.finished)
	}
	want := []string{"commit:ada", "user", "commit:ada2", "compare:ada2"}
	if len(r.calls) != len(want) {
		t.Fatalf("calls %v", r.calls)
	}
	for i := range want {
		if r.calls[i] != want[i] {
			t.Fatalf("calls %v", r.calls)
		}
	}
}

func TestVerifierRenamedAccountStillMissingCounts(t *testing.T) {
	r := &fakeCommitReader{commitErr: map[string]error{"ada": github.ErrNotFound, "ada2": github.ErrNotFound}, user: github.User{Login: "ada2"}}
	s := &fakeCommitStore{leases: []repo.CommitLease{lease(0, false)}}
	_, _ = (&CommitVerifier{Store: s, GitHub: r, Batch: 1}).Tick(context.Background())
	if len(s.finished) != 1 || s.finished[0].State != "pending" || s.finished[0].Tries != 1 {
		t.Fatalf("%+v", s.finished)
	}
}

func TestHasTrailer(t *testing.T) {
	for msg, want := range map[string]bool{
		"pass: x\n\nDojo-Ticket: a/b\nDojo-Event: e": true,
		"pass: x\r\n\r\nDojo-Ticket: a/b\r\n":        true,
		"Dojo-Ticket: a/bc":                          false,
		"Dojo-Ticket: a/b extra":                     false,
		"mentions Dojo-Ticket: a/b in a sentence":    false,
		"": false,
	} {
		if got := hasTrailer(msg, "Dojo-Ticket", "a/b"); got != want {
			t.Errorf("%q: got %v", msg, got)
		}
	}
}

func TestParseCommitVerify(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if d, n, err := ParseCommitVerify(env(nil)); err != nil || d != time.Minute || n != 20 {
		t.Fatalf("defaults: %v %d %v", d, n, err)
	}
	if d, n, err := ParseCommitVerify(env(map[string]string{"DOJO_COMMIT_VERIFY_INTERVAL_SECONDS": "1", "DOJO_COMMIT_VERIFY_BATCH": "5"})); err != nil || d != time.Second || n != 5 {
		t.Fatalf("set: %v %d %v", d, n, err)
	}
	for _, bad := range []map[string]string{
		{"DOJO_COMMIT_VERIFY_INTERVAL_SECONDS": "0"}, {"DOJO_COMMIT_VERIFY_INTERVAL_SECONDS": "x"}, {"DOJO_COMMIT_VERIFY_BATCH": "-1"}, {"DOJO_COMMIT_VERIFY_BATCH": "9999"},
	} {
		if _, _, err := ParseCommitVerify(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
