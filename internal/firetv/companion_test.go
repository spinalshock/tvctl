package firetv

import (
	"testing"

	"github.com/spinalshock/tvctl/internal/device"
)

// A Fire TV's capabilities depend on whether the companion app is there:
// the driver returns a different type, so assertions tell the truth.
func TestCompanionCapabilities(t *testing.T) {
	plain := driver{}.Open(device.Config{Host: "10.0.0.5", Token: "tok"})
	rich := driver{}.Open(device.Config{Host: "10.0.0.5", Token: "tok", CompanionToken: "ctok"})
	for name, has := range map[string]func(device.Device) bool{
		"Player":        func(d device.Device) bool { _, ok := d.(device.Player); return ok },
		"EventSource":   func(d device.Device) bool { _, ok := d.(device.EventSource); return ok },
		"Transport":     func(d device.Device) bool { _, ok := d.(device.Transport); return ok },
		"FieldReader":   func(d device.Device) bool { _, ok := d.(device.FieldReader); return ok },
		"Overlayer":     func(d device.Device) bool { _, ok := d.(device.Overlayer); return ok },
		"Dasher":        func(d device.Device) bool { _, ok := d.(device.Dasher); return ok },
		"Screenshotter": func(d device.Device) bool { _, ok := d.(device.Screenshotter); return ok },
		"HomeDashboard": func(d device.Device) bool { _, ok := d.(device.HomeDashboard); return ok },
		"MediaPlayer":   func(d device.Device) bool { _, ok := d.(device.MediaPlayer); return ok },
		"Toaster":       func(d device.Device) bool { _, ok := d.(device.Toaster); return ok },
		"Nower":         func(d device.Device) bool { _, ok := d.(device.Nower); return ok },
	} {
		if has(plain) {
			t.Errorf("%s: a Fire TV without the companion app must not claim it", name)
		}
		if !has(rich) {
			t.Errorf("%s: a Fire TV with the companion app must offer it", name)
		}
	}
	// Both can install it; both still do the stock things.
	for _, d := range []device.Device{plain, rich} {
		if _, ok := d.(device.Installer); !ok {
			t.Error("Installer missing")
		}
		if _, ok := d.(device.Speaker); !ok {
			t.Error("Speaker missing")
		}
		if _, ok := d.(device.AppNamer); !ok {
			t.Error("AppNamer missing")
		}
	}
}
