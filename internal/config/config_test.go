package config

import (
	"strings"
	"testing"
)

// At home the default (the other home's TV) isn't on this network, so the
// paired TV that is gets picked; an explicit choice is never second-guessed.
func TestResolvePrefersTVOnThisNetwork(t *testing.T) {
	saved := onLocalNet
	defer func() { onLocalNet = saved }()
	onLocalNet = func(h string) bool { return strings.HasPrefix(h, "192.168.29.") }

	s := &Store{Default: "fire", Devices: map[string]Entry{
		"fire": {Name: "Fire TV", Host: "192.168.0.106"},
		"lg":   {Name: "LG", Host: "192.168.29.76"},
	}}
	if id, _, _ := s.Resolve(""); id != "lg" {
		t.Fatalf("default resolved to %q, want the TV on this network", id)
	}
	if id, _, _ := s.Resolve("Fire TV"); id != "fire" {
		t.Fatalf("explicit choice overridden: %q", id)
	}
	onLocalNet = func(string) bool { return true }
	if id, _, _ := s.Resolve(""); id != "fire" {
		t.Fatalf("default ignored when reachable: %q", id)
	}
}

// Two TVs on this network and a default elsewhere: the same one is picked
// every time, not whichever the map happened to yield.
func TestResolveIsDeterministic(t *testing.T) {
	saved := onLocalNet
	defer func() { onLocalNet = saved }()
	onLocalNet = func(h string) bool { return strings.HasPrefix(h, "10.") }
	s := &Store{Default: "far", Devices: map[string]Entry{
		"far": {Host: "192.168.0.5"}, "b-local": {Host: "10.0.0.2"}, "a-local": {Host: "10.0.0.3"},
	}}
	for i := 0; i < 20; i++ {
		if id, _, _ := s.Resolve(""); id != "a-local" {
			t.Fatalf("run %d picked %q", i, id)
		}
	}
}
