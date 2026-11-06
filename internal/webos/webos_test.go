package webos

import (
	"testing"

	"github.com/spinalshock/tvctl/internal/device"
)

func TestNamesAndStates(t *testing.T) {
	if inputName("HDMI_1") != "hdmi1" || inputName("AV_1") != "av1" {
		t.Error("inputName")
	}
	for state, want := range map[string]bool{"Active": true, "Screen Off": true, "Active Standby": false, "Suspend": false, "": false} {
		if awake(state) != want {
			t.Errorf("awake(%q) = %v", state, !want)
		}
	}
	c := &Client{Host: "10.0.0.2"}
	if id, ok := c.AppID("youtube"); !ok || id != youTubeApp {
		t.Errorf("AppID youtube = %q %v", id, ok)
	}
	if _, ok := c.AppID("netflix"); ok {
		t.Error("netflix should come from the TV's own list, not a table")
	}
	if c.AppName("com.webos.app.home") != "Home" || c.AppName("netflix") != "" {
		t.Error("AppName")
	}
	if c.name() != "10.0.0.2" {
		t.Error("name falls back to host")
	}
	// Capabilities the CLI relies on.
	var d device.Device = c
	for _, has := range []bool{
		func() bool { _, ok := d.(device.Sleeper); return ok }(),
		func() bool { _, ok := d.(device.Waker); return ok }(),
		func() bool { _, ok := d.(device.InputSwitcher); return ok }(),
		func() bool { _, ok := d.(device.Toaster); return ok }(),
		func() bool { _, ok := d.(device.Nower); return ok }(),
		func() bool { _, ok := d.(device.MACLearner); return ok }(),
		func() bool { _, ok := d.(device.AppNamer); return ok }(),
		func() bool { _, ok := d.(device.URLOpener); return ok }(),
	} {
		if !has {
			t.Fatal("webOS client lost a capability")
		}
	}
	if _, ok := d.(device.Installer); ok {
		t.Error("webOS must not claim it can take the companion app")
	}
}
