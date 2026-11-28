package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newSub(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey) {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Subscription{Endpoint: endpoint, P256dh: ua.PublicKey().Bytes(), Auth: []byte("0123456789abcdef")}, ua
}

func sender(t *testing.T, srv *httptest.Server) *Sender {
	t.Helper()
	v, err := ParseVAPID(ExampleKey)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(DefaultHosts, []string{srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSender(p, v, "mailto:hello@example.test")
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now }
	return s
}

func TestSend(t *testing.T) {
	var got *http.Request
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		if status == http.StatusFound {
			w.Header().Set("Location", "http://127.0.0.1:1/")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := sender(t, srv)
	sub, ua := newSub(t, srv.URL+"/push/tok")
	msg := Message{Payload: []byte(`{"title":"t","body":"b","url":"/x"}`), TTL: time.Hour, Topic: Topic("check", "r1")}

	res := s.Send(context.Background(), sub, msg)
	if res.Outcome != Delivered || res.Status != 201 || res.Err != nil {
		t.Fatalf("%+v", res)
	}
	h := got.Header
	if h.Get("TTL") != "3600" || h.Get("Urgency") != "normal" || h.Get("Topic") != msg.Topic ||
		h.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(h.Get("Authorization"), "vapid t=") ||
		!strings.Contains(h.Get("Authorization"), ", k=") {
		t.Fatalf("headers %v", h)
	}
	if pt, err := decrypt(body, ua, sub.Auth); err != nil || string(pt) != string(msg.Payload) {
		t.Fatalf("payload %q %v", pt, err)
	}

	for _, tt := range []struct {
		status int
		want   Outcome
	}{{404, Gone}, {410, Gone}, {429, Retry}, {500, Retry}, {503, Retry}, {400, Rejected}, {403, Rejected}, {413, Rejected}, {302, Rejected}} {
		status = tt.status
		if res := s.Send(context.Background(), sub, msg); res.Outcome != tt.want || res.Status != tt.status || res.Err == nil {
			t.Errorf("%d: %+v", tt.status, res)
		}
	}
}

func TestSendFailuresBeforeTheNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request sent")
	}))
	defer srv.Close()
	s := sender(t, srv)
	sub, _ := newSub(t, "https://evil.example/x")
	if res := s.Send(context.Background(), sub, Message{Payload: []byte("x")}); res.Outcome != Rejected || res.Status != 0 {
		t.Errorf("endpoint: %+v", res)
	}
	sub, _ = newSub(t, srv.URL+"/p")
	if res := s.Send(context.Background(), sub, Message{Payload: make([]byte, MaxPayload+1)}); res.Outcome != Rejected {
		t.Errorf("payload: %+v", res)
	}
}

func TestSendNetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s := sender(t, srv)
	sub, _ := newSub(t, srv.URL+"/p/SECRETTOKEN")
	srv.Close()
	res := s.Send(context.Background(), sub, Message{Payload: []byte("x")})
	if res.Outcome != Retry || res.Err == nil || strings.Contains(res.Err.Error(), "SECRETTOKEN") {
		t.Fatalf("%+v", res)
	}
}

func TestSendGuardsAllowedHostResolvingToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request reached loopback")
	}))
	defer srv.Close()
	v, _ := ParseVAPID(ExampleKey)
	p, _ := NewPolicy([]string{"localhost"}, nil)
	s, _ := NewSender(p, v, "mailto:a@b.test")
	sub, _ := newSub(t, "https://localhost/p/SECRETTOKEN")
	res := s.Send(context.Background(), sub, Message{Payload: []byte("x")})
	if res.Outcome != Rejected || !errors.Is(res.Err, ErrEndpoint) || strings.Contains(res.Err.Error(), "SECRETTOKEN") {
		t.Fatalf("%+v", res)
	}
}

func TestNewSenderSubject(t *testing.T) {
	v, _ := ParseVAPID(ExampleKey)
	p, _ := NewPolicy(nil, nil)
	for _, ok := range []string{"mailto:hello@example.test", "https://example.test/contact"} {
		if _, err := NewSender(p, v, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "mailto:", "hello@example.test", "http://example.test", "https://", "javascript:alert(1)"} {
		if _, err := NewSender(p, v, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NewSender(p, nil, "mailto:a@b.test"); err == nil {
		t.Error("nil key accepted")
	}
}
