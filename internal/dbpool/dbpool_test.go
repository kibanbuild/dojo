package dbpool

import (
	"context"
	"net"
	"testing"
	"time"
)

// A connect that never completes must fail within the bound, not the OS SYN timeout.
func TestNewBoundsAConnectThatNeverCompletes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // accepted and never answered: the startup handshake hangs
		}
	}()

	pool, err := New(context.Background(), "postgres://u:p@"+ln.Addr().String()+"/d?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	done := make(chan error, 1)
	go func() { done <- pool.Ping(context.Background()) }() // no deadline on the caller's context
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ping succeeded against a silent server")
		}
	case <-time.After(ConnectTimeout + 5*time.Second):
		t.Fatalf("connect still hanging %s after the bound", ConnectTimeout+5*time.Second)
	}
}

func TestConfigKeepsAnExplicitConnectTimeout(t *testing.T) {
	cfg, err := Config("postgres://u:p@h/d?connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout != 2*time.Second {
		t.Fatalf("connect timeout: %s", cfg.ConnConfig.ConnectTimeout)
	}
	cfg, err = Config("postgres://u:p@h/d")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout != ConnectTimeout {
		t.Fatalf("default connect timeout: %s", cfg.ConnConfig.ConnectTimeout)
	}
}
