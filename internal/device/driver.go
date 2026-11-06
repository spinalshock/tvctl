package device

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// A Driver is one TV brand: how to find its TVs on the LAN and how to build
// a Device for one. Each brand package registers its driver from init, so
// callers only ever talk to this package:
//
//	found, _ := device.Discover(ctx, 2500*time.Millisecond)
//	dev, _ := device.Open(device.Config{Brand: found[0].Brand, Host: found[0].Host})
//
// Everything past that (pairing, wake, URLs, …) is the optional interfaces
// in device.go, asserted on the Device.
type Driver interface {
	Brand() string
	Discover(ctx context.Context, timeout time.Duration) ([]Found, error)
	Open(cfg Config) Device
}

// Found is a TV seen on the LAN, before pairing.
type Found struct {
	Host  string `json:"host"`
	Brand string `json:"brand"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
}

// Config is what a driver needs to talk to one paired TV: the saved entry,
// minus anything brand-neutral code shouldn't care about.
type Config struct {
	ID    string // stable id from a previous pairing; "" before
	Brand string
	Name  string
	Host  string
	MAC   string
	Token string // the driver's opaque pairing secret
	// CompanionToken is the pairing token of the companion add-on on the
	// TV, when one has been installed (Installer); "" otherwise. A driver
	// that has it composes the add-on's capabilities in.
	CompanionToken string
}

// ErrAsleep is returned by passive calls (the TUI's polls) when the TV is
// in standby, so pollers can back off without treating it as a failure.
var ErrAsleep = errors.New("TV is asleep")

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
	legacy  string // brand of entries saved before entries had a brand
)

// Register adds a driver; called from each brand package's init.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	drivers[d.Brand()] = d
}

// RegisterLegacy names the brand that config entries without a brand
// belong to: the one brand that existed before there were several.
func RegisterLegacy(brand string) {
	mu.Lock()
	defer mu.Unlock()
	legacy = brand
}

// Brands lists the registered brands, sorted, for help text and flags.
func Brands() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(drivers))
	for b := range drivers {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Open builds the Device for a paired TV's config.
func Open(cfg Config) (Device, error) {
	mu.RLock()
	if cfg.Brand == "" {
		cfg.Brand = legacy
	}
	d, ok := drivers[cfg.Brand]
	mu.RUnlock()
	if !ok {
		return nil, Failf("no driver for a %s TV", cfg.Brand)
	}
	return d.Open(cfg), nil
}

// Discover asks every registered driver at once and merges what they find.
// One brand failing doesn't hide the others; it's an error only if all fail.
func Discover(ctx context.Context, timeout time.Duration) ([]Found, error) {
	mu.RLock()
	ds := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		ds = append(ds, d)
	}
	mu.RUnlock()

	var (
		wg   sync.WaitGroup
		lock sync.Mutex
		out  []Found
		errs []error
	)
	for _, d := range ds {
		wg.Add(1)
		go func(d Driver) {
			defer wg.Done()
			f, err := d.Discover(ctx, timeout)
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", d.Brand(), err))
				return
			}
			out = append(out, f...)
		}(d)
	}
	wg.Wait()
	if len(out) == 0 && len(errs) == len(ds) && len(errs) > 0 {
		return nil, Fail("couldn't search this network").Because(errors.Join(errs...))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}
