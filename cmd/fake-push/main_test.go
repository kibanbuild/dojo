package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("TTL", "60")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRecordsAndLists(t *testing.T) {
	h := newHandler()
	if rec := do(t, h, "POST", "/push/a", []byte("one")); rec.Code != http.StatusCreated {
		t.Fatalf("push: %d", rec.Code)
	}
	do(t, h, "POST", "/push/a", []byte("two"))
	do(t, h, "POST", "/push/b", []byte("other"))
	var got []push
	rec := do(t, h, "GET", "/_fake/pushes/a", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0].Body) != "one" || string(got[1].Body) != "two" || got[0].Headers["Ttl"][0] != "60" {
		t.Fatalf("%+v", got)
	}
	if rec := do(t, h, "GET", "/_fake/pushes/none", nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list: %q", rec.Body)
	}
}

func TestGoneAnswers410(t *testing.T) {
	h := newHandler()
	if rec := do(t, h, "POST", "/_fake/gone/a", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("gone: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/push/a", []byte("x")); rec.Code != http.StatusGone {
		t.Fatalf("a: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/push/b", []byte("x")); rec.Code != http.StatusCreated {
		t.Fatalf("b: %d", rec.Code)
	}
}

func TestGlobalCaps(t *testing.T) {
	h := newHandler()
	for i := range maxTokens {
		if rec := do(t, h, "POST", "/push/t"+strconv.Itoa(i), []byte("x")); rec.Code != http.StatusCreated {
			t.Fatalf("token %d: %d", i, rec.Code)
		}
	}
	if rec := do(t, h, "POST", "/push/extra", []byte("x")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("token cap: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/push/t0", []byte("x")); rec.Code != http.StatusCreated {
		t.Fatalf("known token: %d", rec.Code)
	}
	for i := range maxTokens {
		do(t, h, "POST", "/_fake/gone/g"+strconv.Itoa(i), nil)
	}
	if rec := do(t, h, "POST", "/_fake/gone/extra", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("gone cap: %d", rec.Code)
	}

	h = newHandler()
	big := make([]byte, maxBody)
	stored := 0
	for i := 0; stored+maxBody <= maxStored; i++ {
		if rec := do(t, h, "POST", "/push/b"+strconv.Itoa(i/maxPerHook), big); rec.Code != http.StatusCreated {
			t.Fatalf("fill %d: %d", i, rec.Code)
		}
		stored += maxBody
	}
	if rec := do(t, h, "POST", "/push/more", big); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("byte cap: %d", rec.Code)
	}
}

func TestLimits(t *testing.T) {
	h := newHandler()
	if rec := do(t, h, "POST", "/push/a", make([]byte, maxBody+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big: %d", rec.Code)
	}
	for range maxPerHook {
		do(t, h, "POST", "/push/a", []byte("x"))
	}
	if rec := do(t, h, "POST", "/push/a", []byte("x")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("cap: %d", rec.Code)
	}
}
