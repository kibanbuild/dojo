package mail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestBuildIsMultipartWithTextFirst(t *testing.T) {
	raw, err := Build(Message{From: "carmen@dojo.test", To: "ada@example.test", Subject: "Welcome, Ada",
		Text: "plain body é", HTML: "<p>html body</p>"}, time.Unix(0, 0), "abc")
	if err != nil {
		t.Fatal(err)
	}
	tp := textproto.NewReader(bufio.NewReader(strings.NewReader(string(raw))))
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("From") != "carmen@dojo.test" || hdr.Get("To") != "ada@example.test" || hdr.Get("Message-Id") != "<abc@dojo.test>" {
		t.Fatalf("headers: %v", hdr)
	}
	typ, params, err := mime.ParseMediaType(hdr.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" {
		t.Fatalf("content type %q %v", typ, err)
	}
	mr := multipart.NewReader(tp.R, params["boundary"])
	var types, bodies []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		types = append(types, p.Header.Get("Content-Type"))
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Fatalf("parts: %v", types)
	}
	if bodies[0] != "plain body é" || bodies[1] != "<p>html body</p>" {
		t.Fatalf("bodies: %q", bodies)
	}
}

func TestBuildRefusesInjection(t *testing.T) {
	ok := Message{From: "carmen@dojo.test", To: "ada@example.test", Subject: "s"}
	for name, m := range map[string]Message{
		"subject newline": {From: ok.From, To: ok.To, Subject: "a\r\nBcc: x@y.z"},
		"to newline":      {From: ok.From, To: "ada@example.test\r\nBcc: x@y.z", Subject: "s"},
		"to display name": {From: ok.From, To: "Ada <ada@example.test>", Subject: "s"},
		"to list":         {From: ok.From, To: "a@b.c, d@e.f", Subject: "s"},
		"from empty":      {To: ok.To, Subject: "s"},
	} {
		if _, err := Build(m, time.Now(), "id"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// fakeSMTP accepts one connection and speaks just enough SMTP to take a message.
func fakeSMTP(t *testing.T, got chan<- string) (host, port string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 ok")
			case cmd == "DATA":
				say("354 go")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					data.WriteString(l)
				}
				got <- data.String()
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				return
			}
		}
	}()
	host, port, _ = net.SplitHostPort(l.Addr().String())
	return host, port
}

func TestSMTPSendDeliversTheMessage(t *testing.T) {
	got := make(chan string, 1)
	host, port := fakeSMTP(t, got)
	s := &SMTP{Host: host, Port: port}
	if err := s.Send(context.Background(), Message{From: "carmen@dojo.test", To: "ada@example.test", Subject: "Hi", Text: "t", HTML: "<p>h</p>"}); err != nil {
		t.Fatal(err)
	}
	if body := <-got; !strings.Contains(body, "Subject: Hi") || !strings.Contains(body, "multipart/alternative") {
		t.Fatalf("data: %q", body)
	}
}

func TestSMTPSendRefusesCredentialsWithoutTLS(t *testing.T) {
	host, port := fakeSMTP(t, make(chan string, 1))
	s := &SMTP{Host: host, Port: port, Username: "u", Password: "p"}
	err := s.Send(context.Background(), Message{From: "carmen@dojo.test", To: "ada@example.test", Subject: "Hi"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want a STARTTLS refusal, got %v", err)
	}
}

// A server that accepts and never speaks must not hang the sender past its timeout.
func TestSMTPSendIsBounded(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(5 * time.Second)
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	s := &SMTP{Host: host, Port: port, Timeout: 300 * time.Millisecond}
	start := time.Now()
	if err := s.Send(context.Background(), Message{From: "carmen@dojo.test", To: "ada@example.test", Subject: "Hi"}); err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestBuildSetsReplyTo(t *testing.T) {
	raw, err := Build(Message{From: "hello@dojo.test", To: "us@dojo.test", ReplyTo: "ada@example.test", Subject: "s"}, time.Unix(0, 0), "abc")
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := textproto.NewReader(bufio.NewReader(strings.NewReader(string(raw)))).ReadMIMEHeader()
	if err != nil || hdr.Get("Reply-To") != "ada@example.test" {
		t.Fatalf("reply-to: %v %v", hdr, err)
	}
	raw, err = Build(Message{From: "hello@dojo.test", To: "us@dojo.test", Subject: "s"}, time.Unix(0, 0), "abc")
	if err != nil || strings.Contains(string(raw), "Reply-To") {
		t.Fatalf("no reply-to expected: %v %q", err, raw)
	}
	for _, bad := range []string{"Ada <ada@example.test>", "a@b.c, d@e.f", "a@b.c\r\nBcc: x@y.z"} {
		if _, err := Build(Message{From: "hello@dojo.test", To: "us@dojo.test", ReplyTo: bad, Subject: "s"}, time.Now(), "id"); !errors.Is(err, ErrBadAddress) {
			t.Errorf("reply-to %q: %v", bad, err)
		}
	}
}

func TestValidAddress(t *testing.T) {
	for addr, want := range map[string]bool{"ada@example.test": true, "": false, "Ada <ada@example.test>": false, "a@b.c, d@e.f": false, "x": false} {
		if got := ValidAddress(addr); got != want {
			t.Errorf("ValidAddress(%q) = %v", addr, got)
		}
	}
}

// Errors name the problem, never the input: callers log them.
func TestBuildErrorsCarryNoAddressOrSubject(t *testing.T) {
	_, err := Build(Message{From: "carmen@dojo.test", To: "secret@example.test, x@y.z", Subject: "s"}, time.Now(), "id")
	if !errors.Is(err, ErrBadAddress) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("address error: %v", err)
	}
	_, err = Build(Message{From: "carmen@dojo.test", To: "a@b.c", Subject: "secret\nBcc: x"}, time.Now(), "id")
	if !errors.Is(err, ErrBadSubject) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("subject error: %v", err)
	}
}
