package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

const (
	maxStreamsPerLearner = 5
	subBuffer            = 32
	hubReconnectDelay    = time.Second
	hubCommandTimeout    = 5 * time.Second
	hubConnectTimeout    = 5 * time.Second
)

// ErrTooManyStreams means the learner already has the maximum of open streams on this task.
var ErrTooManyStreams = errors.New("too many streams")

// Sub is one open stream's mailbox. C carries progress payloads; a lost message or a listener
// drop raises the resync flag and wakes the writer instead.
type Sub struct {
	C       chan []byte
	Wake    chan struct{}
	resync  atomic.Bool
	channel string
}

// TakeResync reports, once, that the stream must send resync.
func (s *Sub) TakeResync() bool { return s.resync.Swap(false) }

func (s *Sub) markResync() {
	s.resync.Store(true)
	select {
	case s.Wake <- struct{}{}:
	default:
	}
}

type hubCmd struct {
	channel string
	done    chan struct{}
}

// Hub is one API task's fan-out: a dedicated listener connection that LISTENs to a learner's
// channel while that learner has an open stream (design 3.8).
type Hub struct {
	connect        func(context.Context) (*pgx.Conn, error)
	connectTimeout time.Duration
	reconnectDelay time.Duration

	mu   sync.Mutex
	subs map[string]map[*Sub]struct{}
	cmds chan hubCmd

	done      chan struct{}
	closeOnce sync.Once
}

// NewHub returns a hub; connect opens the listener connection (outside the pool). Run must be started.
func NewHub(connect func(context.Context) (*pgx.Conn, error)) *Hub {
	return &Hub{connect: connect, connectTimeout: hubConnectTimeout, reconnectDelay: hubReconnectDelay, subs: map[string]map[*Sub]struct{}{}, cmds: make(chan hubCmd), done: make(chan struct{})}
}

// Subscribe registers a stream and returns once its LISTEN is in place.
func (h *Hub) Subscribe(ctx context.Context, learner pgtype.UUID) (*Sub, error) {
	ch := repo.LearnerChannel(learner)
	s := &Sub{C: make(chan []byte, subBuffer), Wake: make(chan struct{}, 1), channel: ch}
	h.mu.Lock()
	if len(h.subs[ch]) >= maxStreamsPerLearner {
		h.mu.Unlock()
		return nil, ErrTooManyStreams
	}
	if h.subs[ch] == nil {
		h.subs[ch] = map[*Sub]struct{}{}
	}
	h.subs[ch][s] = struct{}{}
	h.mu.Unlock()

	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(ctx, hubCommandTimeout)
	defer cancel()
	select {
	case h.cmds <- hubCmd{ch, done}:
	case <-ctx.Done():
		h.Unsubscribe(s)
		return nil, ctx.Err()
	}
	select {
	case <-done:
		return s, nil
	case <-ctx.Done():
		h.Unsubscribe(s)
		return nil, ctx.Err()
	}
}

// Unsubscribe drops a stream; the last one of a learner UNLISTENs the channel.
func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs[s.channel], s)
	if len(h.subs[s.channel]) == 0 {
		delete(h.subs, s.channel)
	}
	h.mu.Unlock()
	// Best effort and off the handler: the listener loop syncs the channel on its next command or reconnect.
	go func() {
		select {
		case h.cmds <- hubCmd{s.channel, make(chan struct{})}:
		case <-h.done:
		case <-time.After(hubCommandTimeout):
		}
	}()
}

// Shutdown ends every open stream (their handlers watch Done); safe to call more than once.
func (h *Hub) Shutdown() { h.closeOnce.Do(func() { close(h.done) }) }

// Done is closed by Shutdown.
func (h *Hub) Done() <-chan struct{} { return h.done }

func (h *Hub) channels() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.subs))
	for ch := range h.subs {
		out = append(out, ch)
	}
	return out
}

func (h *Hub) wanted(ch string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[ch]) > 0
}

func (h *Hub) resyncAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for s := range set {
			s.markResync()
		}
	}
}

func (h *Hub) dispatch(n *pgconn.Notification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[n.Channel] {
		select {
		case s.C <- []byte(n.Payload):
		default:
			s.markResync()
		}
	}
}

// Run keeps the listener connection up until ctx ends. After a drop it reconnects, LISTENs to every
// wanted channel and tells every open stream to resync, since notifications sent meanwhile are gone.
func (h *Hub) Run(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, h.connectTimeout)
		conn, err := h.connect(cctx) // bounded: Subscribe waits behind this loop, and a blackholed dial lasts minutes
		cancel()
		if err == nil {
			listening := map[string]bool{}
			for _, ch := range h.channels() {
				if _, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
					break
				}
				listening[ch] = true
			}
			if err == nil {
				if !first {
					h.resyncAll()
				}
				first = false
				err = h.serve(ctx, conn, listening)
			}
			_ = conn.Close(context.WithoutCancel(ctx))
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("stream listener lost", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(h.reconnectDelay):
		}
	}
}

func (h *Hub) serve(ctx context.Context, conn *pgx.Conn, listening map[string]bool) error {
	type result struct {
		n   *pgconn.Notification
		err error
	}
	for {
		wctx, cancel := context.WithCancel(ctx)
		res := make(chan result, 1)
		go func() {
			n, err := conn.WaitForNotification(wctx)
			res <- result{n, err}
		}()
		var cmd *hubCmd
		var r result
		select {
		case r = <-res:
		case c := <-h.cmds:
			cmd = &c
			cancel()
			r = <-res
		case <-ctx.Done():
			cancel()
			<-res
			return nil
		}
		cancel()
		switch {
		case r.err == nil:
			h.dispatch(r.n)
		case cmd == nil || !errors.Is(r.err, context.Canceled) || conn.IsClosed():
			return r.err
		}
		if cmd != nil {
			if err := h.sync(ctx, conn, listening, cmd.channel); err != nil {
				return err
			}
			close(cmd.done)
		}
	}
}

// sync makes the connection's LISTEN state for one channel match the open streams.
func (h *Hub) sync(ctx context.Context, conn *pgx.Conn, listening map[string]bool, ch string) error {
	want := h.wanted(ch)
	if want == listening[ch] {
		return nil
	}
	verb := "LISTEN "
	if !want {
		verb = "UNLISTEN "
	}
	if _, err := conn.Exec(ctx, verb+pgx.Identifier{ch}.Sanitize()); err != nil {
		return err
	}
	if want {
		listening[ch] = true
	} else {
		delete(listening, ch)
	}
	return nil
}
