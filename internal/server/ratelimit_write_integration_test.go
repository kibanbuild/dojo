//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

func TestPatchMeIsRateLimitedPerLearner(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")
	// A fresh client IP per call: only a per-learner budget can stop them.
	var limited int
	for i := 0; i < 15; i++ {
		code, _ := newCLIClient(t).call(http.MethodPatch, "/v1/me", tok, map[string]any{"language": "en"})
		if code == http.StatusTooManyRequests {
			limited++
		} else if code != http.StatusOK {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if limited != 5 {
		t.Fatalf("%d of 15 calls limited, want 5 past the burst of 10", limited)
	}
}
