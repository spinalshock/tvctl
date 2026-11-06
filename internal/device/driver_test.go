package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeDriver struct {
	brand string
	found []Found
	err   error
}

func (f fakeDriver) Brand() string { return f.brand }
func (f fakeDriver) Discover(context.Context, time.Duration) ([]Found, error) {
	return f.found, f.err
}
func (f fakeDriver) Open(Config) Device { return nil }

// One brand failing to search doesn't hide the TVs another brand found.
func TestDiscoverMergesAndToleratesFailures(t *testing.T) {
	mu.Lock()
	saved := drivers
	drivers = map[string]Driver{}
	mu.Unlock()
	defer func() { mu.Lock(); drivers = saved; mu.Unlock() }()

	Register(fakeDriver{brand: "a", found: []Found{{Host: "10.0.0.9", Brand: "a"}}})
	Register(fakeDriver{brand: "b", err: errors.New("no multicast")})
	Register(fakeDriver{brand: "c", found: []Found{{Host: "10.0.0.2", Brand: "c"}}})
	got, err := Discover(context.Background(), time.Second)
	if err != nil || len(got) != 2 || got[0].Host != "10.0.0.2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := Open(Config{Brand: "zzz"}); err == nil {
		t.Fatal("unknown brand opened")
	}
}
