package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/mail"
	"github.com/kibanyadev/dojo/internal/server/repo"
)

type fakeQueue struct {
	rows      []repo.ListUnsentEmailsRow
	to        map[[16]byte]string
	listed    []string
	delivered []string
	held      map[[16]byte]bool // another task holds the row
	gone      map[[16]byte]bool // the row vanishes before the mark
	sent      map[[16]byte]bool
}

func (q *fakeQueue) ListUnsentEmails(_ context.Context, kinds []string, skip []pgtype.UUID, max int32) ([]repo.ListUnsentEmailsRow, error) {
	q.listed = kinds
	var out []repo.ListUnsentEmailsRow
rows:
	for _, r := range q.rows {
		for _, s := range skip {
			if s == r.ID {
				continue rows
			}
		}
		if q.sent[r.ID.Bytes] {
			continue
		}
		if len(out) < int(max) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (q *fakeQueue) DeliverEmail(ctx context.Context, id pgtype.UUID, send func(context.Context, repo.Email) error) (repo.Delivery, error) {
	if q.held[id.Bytes] {
		return repo.Skipped, nil
	}
	for _, r := range q.rows {
		if r.ID == id {
			if err := send(ctx, repo.Email{ID: r.ID, LearnerID: r.LearnerID, Kind: r.Kind, To: q.to[id.Bytes]}); err != nil {
				return repo.Skipped, err
			}
			q.delivered = append(q.delivered, q.to[id.Bytes])
			if q.sent == nil {
				q.sent = map[[16]byte]bool{}
			}
			q.sent[id.Bytes] = true
			if q.gone[id.Bytes] {
				return repo.SentUnmarked, nil
			}
			return repo.Sent, nil
		}
	}
	return repo.Skipped, nil
}

type fakeReader struct {
	paths     []repo.ListCatalogPathsRow
	pathRoles []repo.ListCatalogPathRolesRow
	roles     []repo.ListCatalogRolesRow
	chosen    string
	err       error
}

func (f fakeReader) GetLearnerByID(context.Context, pgtype.UUID) (repo.Learner, error) {
	return repo.Learner{Username: "ada", GithubLogin: "Ada-GH"}, f.err
}

func (f fakeReader) ListCatalogPaths(context.Context) ([]repo.ListCatalogPathsRow, error) {
	return f.paths, nil
}

func (f fakeReader) ListCatalogPathRoles(context.Context) ([]repo.ListCatalogPathRolesRow, error) {
	return f.pathRoles, nil
}

func (f fakeReader) ListCatalogRoles(context.Context) ([]repo.ListCatalogRolesRow, error) {
	return f.roles, nil
}

func (f fakeReader) GetLearnerChosenPathSlug(context.Context, pgtype.UUID) (string, error) {
	return f.chosen, nil
}

type fakeMail struct {
	calls int
	sent  []mail.Message
	fail  map[string]error // by recipient
}

func (f *fakeMail) Send(_ context.Context, m mail.Message) error {
	f.calls++
	if err := f.fail[m.To]; err != nil {
		return err
	}
	f.sent = append(f.sent, m)
	return nil
}

func uid(n byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{n}, Valid: true} }

func newOutbox(q *fakeQueue, m mail.Sender, paths ...repo.ListCatalogPathsRow) *Outbox {
	return &Outbox{Queue: q, Reader: fakeReader{paths: paths}, Mail: m, Domain: "dojo.test", Origin: "http://localhost:8080"}
}

func TestSweepSendsOnlyTemplatedKinds(t *testing.T) {
	q := &fakeQueue{rows: []repo.ListUnsentEmailsRow{{ID: uid(1), LearnerID: uid(9), Kind: "E1"}}, to: map[[16]byte]string{uid(1).Bytes: "ada@example.test"}}
	m := &fakeMail{}
	o := newOutbox(q, m, repo.ListCatalogPathsRow{Slug: "sysops", Title: "Systems and DevOps", Status: "available"})
	n, err := o.Sweep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sent %d, %v", n, err)
	}
	if len(q.listed) != 1 || q.listed[0] != "E1" {
		t.Fatalf("asked for kinds %v: only E1 has a template", q.listed)
	}
	got := m.sent[0]
	if got.From != "carmen@dojo.test" || got.To != "ada@example.test" || got.Subject != "Welcome to Dojo" {
		t.Fatalf("message: %+v", got)
	}
	if !strings.Contains(got.Text, "Systems and DevOps path") || !strings.Contains(got.Text, "http://localhost:8080/app") {
		t.Fatalf("text: %q", got.Text)
	}
	if !strings.Contains(got.HTML, "Systems and DevOps path") || !strings.Contains(got.HTML, `href="http://localhost:8080/app"`) {
		t.Fatalf("html: %q", got.HTML)
	}
}

func TestWelcomeNamesTheFirstAvailablePath(t *testing.T) {
	q := &fakeQueue{rows: []repo.ListUnsentEmailsRow{{ID: uid(1), LearnerID: uid(9), Kind: "E1"}}, to: map[[16]byte]string{uid(1).Bytes: "a@b.test"}}
	m := &fakeMail{}
	o := newOutbox(q, m,
		repo.ListCatalogPathsRow{Title: "Cloud", Status: "planned"},
		repo.ListCatalogPathsRow{Title: "Systems", Status: "available"},
		repo.ListCatalogPathsRow{Title: "Other", Status: "available"})
	if _, err := o.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.sent[0].Text, "Systems path") || strings.Contains(m.sent[0].Text, "Cloud") {
		t.Fatalf("text: %q", m.sent[0].Text)
	}
}

func TestWelcomeWithoutAPathStillReads(t *testing.T) {
	text := welcomeText(welcomeData{Username: "ada", Link: "http://x/app"})
	if strings.Contains(text, "path") || !strings.Contains(text, "Your account is ready.") {
		t.Fatalf("text: %q", text)
	}
}

func TestWelcomePathPick(t *testing.T) {
	paths := []repo.ListCatalogPathsRow{
		{Slug: "cloud", Title: "Cloud", Status: "planned"},
		{Slug: "sysops", Title: "Systems", Status: "available"},
		{Slug: "backend", Title: "Backend", Status: "available"},
	}
	pathRoles := []repo.ListCatalogPathRolesRow{
		{Path: "sysops", Role: "support", Position: 2},
		{Path: "sysops", Role: "foundations", Position: 1},
		{Path: "backend", Role: "apis", Position: 1},
	}
	roles := []repo.ListCatalogRolesRow{{Slug: "support", Title: "Support"}, {Slug: "foundations", Title: "Foundations"}, {Slug: "apis", Title: "APIs"}}
	for _, tt := range []struct{ name, chosen, wantPath, wantRole string }{
		{"no choice takes the first available", "", "Systems", "Foundations"},
		{"the chosen path wins", "backend", "Backend", "APIs"},
		{"a planned choice is ignored", "cloud", "Systems", "Foundations"},
		{"an unknown choice is ignored", "gone", "Systems", "Foundations"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, r := welcomePath(paths, tt.chosen, pathRoles, roles)
			if p != tt.wantPath || r != tt.wantRole {
				t.Fatalf("got %q, %q", p, r)
			}
		})
	}
	if p, r := welcomePath(nil, "", nil, nil); p != "" || r != "" {
		t.Fatalf("empty catalog: %q, %q", p, r)
	}
	if p, r := welcomePath(paths, "", nil, nil); p != "Systems" || r != "" {
		t.Fatalf("path without roles: %q, %q", p, r)
	}
}

func TestWelcomeBodyFollowsTheDoc(t *testing.T) {
	d := welcomeData{Username: "ada", GithubLogin: "Ada-GH", PathTitle: "Systems", RoleTitle: "Foundations",
		Link: "http://x/app", InstallCmd: welcomeInstallCmd("http://x")}
	html, err := renderWelcomeHTML(d)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"text": welcomeText(d), "html": html} {
		for _, want := range []string{"Systems path", "first role is Foundations", "curl -fsSL http://x/install | sh", "dojo login", "dojo doctor",
			"Labs run on your computer, and the work stays in your own Ada-GH/dojo repo.", "If you did not sign up, ignore this message."} {
			if !strings.Contains(body, want) {
				t.Errorf("%s misses %q: %q", name, want, body)
			}
		}
		if strings.Contains(strings.ToLower(body), "streak") || strings.Contains(body, "<img") {
			t.Errorf("%s has tracking or streak language", name)
		}
	}
}

func TestWelcomeEscapesHTMLAndHasNoEmDash(t *testing.T) {
	d := welcomeData{Username: `<script>x</script>`, PathTitle: "A & B", Link: "http://x/app"}
	html, err := renderWelcomeHTML(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || !strings.Contains(html, "A &amp; B") {
		t.Fatalf("html: %q", html)
	}
	for _, s := range []string{html, welcomeText(d), welcomeSubject} {
		if strings.ContainsRune(s, '—') {
			t.Fatalf("em dash in %q", s)
		}
	}
	for _, want := range []string{"font-size:16px", "text-decoration:underline", `role="presentation"`} {
		if !strings.Contains(html, want) {
			t.Errorf("html misses %q", want)
		}
	}
}

func TestSweepSkipsAHeldRowAndKeepsGoingAfterAFailure(t *testing.T) {
	q := &fakeQueue{
		rows: []repo.ListUnsentEmailsRow{
			{ID: uid(1), LearnerID: uid(9), Kind: "E1"}, {ID: uid(2), LearnerID: uid(9), Kind: "E1"}, {ID: uid(3), LearnerID: uid(9), Kind: "E1"}},
		to:   map[[16]byte]string{uid(1).Bytes: "held@x.test", uid(2).Bytes: "bad@x.test", uid(3).Bytes: "ok@x.test"},
		held: map[[16]byte]bool{uid(1).Bytes: true},
	}
	m := &fakeMail{fail: map[string]error{"bad@x.test": errors.New("550 no")}}
	o := newOutbox(q, m)
	n, err := o.Sweep(context.Background())
	if err != nil || n != 1 || len(m.sent) != 1 || m.sent[0].To != "ok@x.test" {
		t.Fatalf("sent %d %v, %v", n, m.sent, err)
	}
}

func TestSweepBacksOffAFailedRow(t *testing.T) {
	q := &fakeQueue{rows: []repo.ListUnsentEmailsRow{{ID: uid(2), LearnerID: uid(9), Kind: "E1"}}, to: map[[16]byte]string{uid(2).Bytes: "bad@x.test"}}
	m := &fakeMail{fail: map[string]error{"bad@x.test": errors.New("550 no")}}
	now := time.Unix(1_800_000_000, 0)
	o := newOutbox(q, m)
	o.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		_, _ = o.Sweep(context.Background())
	}
	if m.calls != 1 {
		t.Fatalf("a failed row was retried inside the backoff: %d attempts", m.calls)
	}
	now = now.Add(transientBackoff)
	_, _ = o.Sweep(context.Background())
	if m.calls != 2 {
		t.Fatalf("a failed row was not retried after the backoff: %d attempts", m.calls)
	}
}

// Failing rows must not fill the batch: with more of them than the batch size ahead of a good row, the
// good row still goes out.
func TestSweepNotStarvedByMoreFailuresThanTheBatch(t *testing.T) {
	q := &fakeQueue{to: map[[16]byte]string{}}
	m := &fakeMail{fail: map[string]error{}}
	for i := 1; i <= outboxBatch+5; i++ {
		id := uid(byte(i))
		addr := fmt.Sprintf("bad%d@x.test", i)
		q.rows = append(q.rows, repo.ListUnsentEmailsRow{ID: id, LearnerID: uid(200), Kind: "E1"})
		q.to[id.Bytes] = addr
		m.fail[addr] = &textproto.Error{Code: 550, Msg: "no such user"}
	}
	good := uid(100)
	q.rows = append(q.rows, repo.ListUnsentEmailsRow{ID: good, LearnerID: uid(200), Kind: "E1"})
	q.to[good.Bytes] = "good@x.test"
	o := newOutbox(q, m)
	for i := 0; i < 3 && len(q.delivered) == 0; i++ {
		if _, err := o.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.delivered) != 1 || q.delivered[0] != "good@x.test" {
		t.Fatalf("delivered %v, want the good row", q.delivered)
	}
}

func TestPermanentFailureBacksOffAnHourAndTransientFiveMinutes(t *testing.T) {
	q := &fakeQueue{
		rows: []repo.ListUnsentEmailsRow{{ID: uid(1), LearnerID: uid(9), Kind: "E1"}, {ID: uid(2), LearnerID: uid(9), Kind: "E1"}},
		to:   map[[16]byte]string{uid(1).Bytes: "perm@x.test", uid(2).Bytes: "temp@x.test"},
	}
	m := &fakeMail{fail: map[string]error{
		"perm@x.test": fmt.Errorf("smtp rcpt: %w", &textproto.Error{Code: 550, Msg: "no"}),
		"temp@x.test": fmt.Errorf("smtp rcpt: %w", &textproto.Error{Code: 451, Msg: "later"}),
	}}
	now := time.Unix(1_800_000_000, 0)
	o := newOutbox(q, m)
	o.Now = func() time.Time { return now }
	_, _ = o.Sweep(context.Background())
	if m.calls != 2 {
		t.Fatalf("first sweep tried %d rows", m.calls)
	}
	now = now.Add(transientBackoff)
	_, _ = o.Sweep(context.Background())
	if m.calls != 3 {
		t.Fatalf("after 5 minutes only the transient row should retry: %d calls", m.calls)
	}
	now = now.Add(permanentBackoff)
	_, _ = o.Sweep(context.Background())
	if m.calls != 5 {
		t.Fatalf("after an hour both rows should retry: %d calls", m.calls)
	}
}

// A row that vanishes between send and mark (learner deleted mid-send) still counts as sent and is not retried.
func TestSweepCountsAMailSentWhoseRowWasGone(t *testing.T) {
	q := &fakeQueue{rows: []repo.ListUnsentEmailsRow{{ID: uid(1), LearnerID: uid(9), Kind: "E1"}},
		to: map[[16]byte]string{uid(1).Bytes: "a@x.test"}, gone: map[[16]byte]bool{uid(1).Bytes: true}}
	m := &fakeMail{}
	o := newOutbox(q, m)
	n, err := o.Sweep(context.Background())
	if err != nil || n != 1 || len(o.retryAt) != 0 {
		t.Fatalf("sent %d, retry %v, %v", n, o.retryAt, err)
	}
}

// No log line may carry a recipient: not for an invalid address, and not for a server reply that echoes it.
func TestFailureLogsNeverCarryTheAddress(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	const bad, echoed = "secret.bad@example.test, other@example.test", "secret.echo@example.test"
	q := &fakeQueue{
		rows: []repo.ListUnsentEmailsRow{{ID: uid(1), LearnerID: uid(9), Kind: "E1"}, {ID: uid(2), LearnerID: uid(9), Kind: "E1"}},
		to:   map[[16]byte]string{uid(1).Bytes: bad, uid(2).Bytes: echoed},
	}
	// The real SMTP sender refuses the bad address while building the message, before any dial.
	real := &mail.SMTP{Host: "127.0.0.1", Port: "1"}
	echo := &fakeMail{fail: map[string]error{echoed: fmt.Errorf("smtp rcpt: %w", &textproto.Error{Code: 550, Msg: "no such user " + echoed})}}
	o := newOutbox(q, senderFunc(func(ctx context.Context, m mail.Message) error {
		if m.To == echoed {
			return echo.Send(ctx, m)
		}
		return real.Send(ctx, m)
	}))
	now := time.Unix(1_800_000_000, 0)
	o.Now = func() time.Time { return now }
	if _, err := o.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	if strings.Contains(log, "secret") || strings.Contains(log, "example.test") {
		t.Fatalf("log carries an address:\n%s", log)
	}
	if !strings.Contains(log, "invalid_address") || !strings.Contains(log, "smtp_code=550") {
		t.Fatalf("log misses the classification:\n%s", log)
	}
	for id, at := range o.retryAt {
		if !at.Equal(now.Add(permanentBackoff)) {
			t.Errorf("row %x backs off until %v, want an hour", id[:1], at)
		}
	}
}

type senderFunc func(context.Context, mail.Message) error

func (f senderFunc) Send(ctx context.Context, m mail.Message) error { return f(ctx, m) }
