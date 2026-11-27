package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/github"
)

type fakeLookup struct {
	user  github.User
	err   error
	calls int
}

func (f *fakeLookup) UserByLogin(_ context.Context, _ string) (github.User, error) {
	f.calls++
	return f.user, f.err
}

type fakeBootstrapStore struct {
	err    error
	gotID  int64
	gotLog string
	calls  int
}

func (f *fakeBootstrapStore) CreateFirstOperator(_ context.Context, id int64, login string) (string, error) {
	f.calls++
	f.gotID, f.gotLog = id, login
	if f.err != nil {
		return "", f.err
	}
	return "op-1", nil
}

func TestValidLogin(t *testing.T) {
	tests := []struct {
		login string
		ok    bool
	}{
		{"ada", true}, {"a", true}, {"Ada-Lovelace", true}, {"a1-b2", true}, {strings.Repeat("a", 39), true},
		{"", false}, {strings.Repeat("a", 40), false}, {"-ada", false}, {"ada-", false}, {"a--b", false},
		{"a_b", false}, {"a b", false}, {"ada\n", false}, {"ada/../x", false}, {"adá", false}, {"@ada", false},
	}
	for _, tt := range tests {
		if got := ValidLogin(tt.login); got != tt.ok {
			t.Errorf("ValidLogin(%q) = %v, want %v", tt.login, got, tt.ok)
		}
	}
}

func TestBootstrap(t *testing.T) {
	tests := []struct {
		name      string
		login     string
		lookup    fakeLookup
		storeErr  error
		wantErr   error
		wantID    string
		wantCalls [2]int // lookup, store
	}{
		{"ok stores the canonical login", "ada", fakeLookup{user: github.User{ID: 1001, Login: "Ada"}}, nil, nil, "op-1", [2]int{1, 1}},
		{"invalid login never reaches the network", "-bad-", fakeLookup{}, nil, ErrInvalidLogin, "", [2]int{0, 0}},
		{"unknown login", "nobody", fakeLookup{err: github.ErrNoSuchUser}, nil, github.ErrNoSuchUser, "", [2]int{1, 0}},
		{"github down", "ada", fakeLookup{err: github.ErrUnavailable}, nil, github.ErrUnavailable, "", [2]int{1, 0}},
		{"operator exists", "ada", fakeLookup{user: github.User{ID: 1001, Login: "ada"}}, ErrOperatorsExist, ErrOperatorsExist, "", [2]int{1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeBootstrapStore{err: tt.storeErr}
			id, err := Bootstrap(context.Background(), &tt.lookup, st, tt.login)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) || tt.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if id != tt.wantID || tt.lookup.calls != tt.wantCalls[0] || st.calls != tt.wantCalls[1] {
				t.Fatalf("id %q lookups %d stores %d", id, tt.lookup.calls, st.calls)
			}
			if tt.wantErr == nil && (st.gotID != 1001 || st.gotLog != "Ada") {
				t.Fatalf("stored %d %q", st.gotID, st.gotLog)
			}
		})
	}
}
