package firetv

import (
	"context"
	"time"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/receiver"
)

func init() {
	device.Register(driver{})
	device.RegisterLegacy(brand) // entries saved before brands existed are Fire TVs
}

// driver plugs Fire TV into the device registry.
type driver struct{}

func (driver) Brand() string { return brand }

func (driver) Discover(ctx context.Context, timeout time.Duration) ([]device.Found, error) {
	found, err := Discover(ctx, timeout)
	if err != nil {
		return nil, err
	}
	out := make([]device.Found, len(found))
	for i, f := range found {
		out[i] = device.Found{Host: f.Host, Brand: brand, Name: f.Name}
	}
	return out, nil
}

// Open returns the stock client, or, when the companion app has been
// installed on this TV, the client composed with it (companion.go).
func (driver) Open(cfg device.Config) device.Device {
	c := New(cfg.Host, cfg.Name, cfg.Token)
	c.MAC = cfg.MAC
	c.Serial = cfg.ID
	c.companionToken = cfg.CompanionToken
	if cfg.CompanionToken == "" {
		return c
	}
	return &withCompanion{Client: c, rc: receiver.New(cfg.Host, cfg.CompanionToken)}
}
