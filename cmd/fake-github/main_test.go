package main

import (
	"testing"

	"github.com/kibanbuild/dojo/internal/fakegithub"
)

func TestEnableDevice(t *testing.T) {
	mk := func() []fakegithub.Client { return []fakegithub.Client{{ID: "a"}, {ID: "b"}} }
	cs := mk()
	if err := enableDevice(cs, "b"); err != nil || cs[0].DeviceFlow || !cs[1].DeviceFlow {
		t.Fatalf("got %v %+v", err, cs)
	}
	cs = mk()
	if err := enableDevice(cs, "a, b"); err != nil || !cs[0].DeviceFlow || !cs[1].DeviceFlow {
		t.Fatalf("list: %v %+v", err, cs)
	}
	if err := enableDevice(mk(), "nope"); err == nil {
		t.Fatal("unknown client accepted")
	}
	cs = mk()
	if err := enableDevice(cs, ""); err != nil || cs[0].DeviceFlow {
		t.Fatalf("empty: %v", err)
	}
}
