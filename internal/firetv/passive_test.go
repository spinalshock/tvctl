package firetv

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A keyboard probe against a TV that isn't listening must report ErrAsleep
// without trying to start (and so wake) the remote service.
func TestKeyboardOpenIsPassive(t *testing.T) {
	// 127.0.0.2: loopback range, but nothing is bound there, unlike 127.0.0.1
	// where a developer machine may well have something on :8080.
	c := New("127.0.0.2", "test", "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := c.KeyboardOpen(ctx)
	if !errors.Is(err, ErrAsleep) {
		t.Fatalf("want ErrAsleep, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("probe took %v; a passive probe must not wait for a kick", time.Since(start))
	}
}
