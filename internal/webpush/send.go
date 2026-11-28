package webpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Outcome tells the worker what to do with a send.
type Outcome int

const (
	// Delivered: the push service accepted the message.
	Delivered Outcome = iota
	// Gone: the subscription is dead (404 or 410); delete it.
	Gone
	// Retry: 429, 5xx or a network error; the attempt failed but may succeed later.
	Retry
	// Rejected: any other refusal, including a refused endpoint; log it, do not retry.
	Rejected
)

// Result is what Send reports; Status is 0 when no response arrived.
type Result struct {
	Outcome Outcome
	Status  int
	Err     error
}

// Subscription is a browser's PushSubscription with keys decoded.
type Subscription struct {
	Endpoint string
	P256dh   []byte
	Auth     []byte
}

// Message is one push. TTL is rounded down to whole seconds.
type Message struct {
	Payload []byte
	TTL     time.Duration
	Topic   string
}

// Sender posts messages under a Policy and a VAPID key.
type Sender struct {
	Policy  Policy
	VAPID   *VAPID
	Subject string
	Now     func() time.Time

	guarded, open *http.Client
}

// NewSender builds a Sender whose guarded client refuses private addresses at connect time.
// The subject must be a mailto: or https: URI (RFC 8292 section 2.1).
func NewSender(p Policy, v *VAPID, subject string) (*Sender, error) {
	if v == nil {
		return nil, errors.New("webpush: VAPID key required")
	}
	if rest, ok := strings.CutPrefix(subject, "mailto:"); !ok || rest == "" {
		if u, err := url.Parse(subject); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, errors.New("webpush: subject must be a mailto: or https: URI")
		}
	}
	return &Sender{Policy: p, VAPID: v, Subject: subject, Now: time.Now, guarded: client(guard), open: client(nil)}, nil
}

// cause drops the request URL, which carries the subscription's secret path, from a client error.
func cause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("webpush: send failed: %w", err)
}

func client(control func(string, string, syscall.RawConn) error) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Send encrypts and posts one message.
func (s *Sender) Send(ctx context.Context, sub Subscription, m Message) Result {
	u, err := s.Policy.parse(sub.Endpoint)
	if err != nil {
		return Result{Outcome: Rejected, Err: err}
	}
	body, err := Encrypt(m.Payload, sub.P256dh, sub.Auth)
	if err != nil {
		return Result{Outcome: Rejected, Err: err}
	}
	auth, err := s.VAPID.Header(u.Scheme+"://"+u.Host, s.Subject, s.Now())
	if err != nil {
		return Result{Outcome: Rejected, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: Rejected, Err: errors.New("webpush: bad request")}
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(m.TTL/time.Second), 10))
	req.Header.Set("Urgency", "normal")
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	c := s.guarded
	if s.Policy.isOrigin(u) {
		c = s.open
	}
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, ErrEndpoint) {
			return Result{Outcome: Rejected, Err: cause(err)}
		}
		return Result{Outcome: Retry, Err: cause(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return Result{Outcome: classify(resp.StatusCode), Status: resp.StatusCode, Err: statusErr(resp.StatusCode)}
}

func classify(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return Delivered
	case status == http.StatusNotFound || status == http.StatusGone:
		return Gone
	case status == http.StatusTooManyRequests || status >= 500:
		return Retry
	}
	return Rejected
}

func statusErr(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("webpush: push service answered %d", status)
}
