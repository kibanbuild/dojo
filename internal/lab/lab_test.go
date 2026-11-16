package lab

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	b, err := New(DefaultName())
	if err != nil || b.Name() != "lima" {
		t.Fatalf("default backend = %v, %v", b, err)
	}
	_, err = New("nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "known: lima") {
		t.Fatalf("unknown backend error = %v", err)
	}
}

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb", 5, "a\nb"},
		{"", 3, ""},
	} {
		if got := lastLines(tc.in, tc.n); got != tc.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
