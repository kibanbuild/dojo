// Package mail is the adapter for outbound email: a Sender interface and an SMTP implementation
// that speaks to Mailpit locally and to Amazon SES in production (design 1.7).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// ErrBadAddress and ErrBadSubject are what Build returns for input it refuses. They carry no part of the
// input, so a caller can log them without leaking an address or a header value.
var (
	ErrBadAddress = errors.New("not a bare email address")
	ErrBadSubject = errors.New("subject has a line break")
)

// Message is one email with a plain-text and an HTML part.
type Message struct {
	From    string // bare address
	To      string // bare address
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message. A nil error means the server accepted it.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends over one SMTP connection per message. Every step is bounded: the dial by DialTimeout and
// the whole exchange by Timeout, so a host that resolves to nothing never hangs a caller.
type SMTP struct {
	Host     string
	Port     string
	Username string
	Password string
	// DialTimeout and Timeout default to 5 and 20 seconds.
	DialTimeout time.Duration
	Timeout     time.Duration
}

// Send builds the message and hands it to the server. With credentials it insists on STARTTLS, so a
// password never travels in clear; without them (Mailpit) TLS is used when the server offers it.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	raw, err := Build(m, time.Now(), randomID())
	if err != nil {
		return err
	}
	dial, total := s.DialTimeout, s.Timeout
	if dial <= 0 {
		dial = 5 * time.Second
	}
	if total <= 0 {
		total = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	d := net.Dialer{Timeout: dial}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, s.Port))
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Closing the connection on cancel unblocks a read stuck in the exchange.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	} else if s.Username != "" {
		return errors.New("smtp: credentials set but the server offers no STARTTLS")
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data end: %w", err)
	}
	return c.Quit()
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Build renders the RFC 5322 message: multipart/alternative with a plain-text part first and an HTML
// part second, both quoted-printable UTF-8. id makes the Message-ID unique.
func Build(m Message, now time.Time, id string) ([]byte, error) {
	from, err := bare(m.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := bare(m.To)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, ErrBadSubject
	}
	domain := from[strings.LastIndex(from, "@")+1:]
	boundary := "dojo-" + id
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", now.UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+id+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, p := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + p.typ + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err := writeQP(&b, p.body); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func writeQP(w io.Writer, s string) error {
	q := quotedprintable.NewWriter(w)
	if _, err := q.Write([]byte(s)); err != nil {
		return err
	}
	return q.Close()
}

// bare returns the address alone and refuses a display name, a list or a line break.
func bare(v string) (string, error) {
	a, err := mail.ParseAddress(v)
	if err != nil || a.Name != "" || a.Address != v {
		return "", ErrBadAddress
	}
	return a.Address, nil
}
