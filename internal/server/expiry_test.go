package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExpirer struct {
	calls atomic.Int32
	err   error
}

func (f *fakeExpirer) ExpireLabs(context.Context) (int, error) {
	f.calls.Add(1)
	return 1, f.err
}

func TestRunExpirySweepsAtStartup(t *testing.T) {
	f := &fakeExpirer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunExpiry(ctx, f, time.Hour); close(done) }()
	deadline := time.After(2 * time.Second)
	for f.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("no sweep at startup")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunExpirySweepsOnEveryTickAndStopsWithContext(t *testing.T) {
	f := &fakeExpirer{err: errors.New("db down")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunExpiry(ctx, f, 5*time.Millisecond); close(done) }()
	deadline := time.After(2 * time.Second)
	for f.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d sweeps: an error must not stop the loop", f.calls.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunExpiry did not stop with its context")
	}
}
