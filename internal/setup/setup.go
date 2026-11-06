// Package setup installs the companion app on an Android-based TV over
// ADB and grants it the permissions Android offers no settings screen
// for. It is the only place the ADB protocol is used, and the only step
// that asks the user to touch the TV's developer settings. Everything
// afterwards is plain HTTP. Drivers of Android-based TVs expose it as
// device.Installer.
package setup

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/spinalshock/tvctl/internal/adb"
	"github.com/spinalshock/tvctl/internal/config"
	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
	"github.com/spinalshock/tvctl/internal/receiver"
)

//go:embed receiver.apk
var apk []byte

const (
	pkg      = receiver.Package
	adbPort  = "5555"
	a11ySvc  = pkg + "/.TvctlAccessibilityService"
	listener = pkg + "/.NowPlayingListener"
)

// Options carries the host callbacks: where to print progress and how to
// ask questions. Launch brings the app forward through the TV's own API
// after install (so the boot receiver gets registered).
type Options struct {
	Out    io.Writer
	Ask    func(prompt string) (string, error)
	Launch func(ctx context.Context, pkg string) error
}

// Run performs the whole flow against the TV at host and returns the
// receiver pairing token to persist. An existing, still-valid token is kept
// so re-running setup to update the app doesn't ask for a new PIN.
func Run(ctx context.Context, host, existingToken string, o Options) (string, error) {
	say := func(f string, a ...any) { fmt.Fprintf(o.Out, f+"\n", a...) }
	addr := net.JoinHostPort(host, adbPort)

	// 1. Wait for debugging to be switched on.
	if !netx.PortOpen(addr) {
		say("On the TV, turn on debugging once:")
		say("  Settings › My Fire TV (or Device Preferences) › Developer Options › ADB debugging › ON")
		say("  (No Developer Options? In About, press Select 7 times on the device name.)")
		say("Waiting for the TV to accept connections on port %s …", adbPort)
		for !netx.PortOpen(addr) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	// 2. Connect; the first time, the TV shows an "Allow?" prompt.
	cfgPath, err := config.Path()
	if err != nil {
		return "", err
	}
	key, err := adb.LoadOrCreateKey(filepath.Join(filepath.Dir(cfgPath), "adbkey"))
	if err != nil {
		return "", err
	}
	say("Connecting …")
	conn, err := adb.Dial(ctx, addr, key, func() {
		say("The TV is asking \"Allow USB debugging?\" — choose Always allow, then OK.")
	}, 120*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// 3. Install (or update).
	say("Installing tvctl receiver (%.1f MB) …", float64(len(apk))/1e6)
	last := -1
	if _, err := conn.Install(ctx, apk, func(sent, total int) {
		pct := sent * 100 / total
		if pct/10 != last/10 {
			last = pct
			fmt.Fprintf(o.Out, "  %d%%\r", pct)
		}
	}); err != nil {
		return "", err
	}
	say("  installed")

	// 4. Permissions. Each is idempotent; failures are reported, not fatal,
	// because the app degrades gracefully without them.
	say("Granting permissions …")
	grants := []struct{ what, cmd string }{
		{"now-playing from other apps", "cmd notification allow_listener " + listener},
		{"overlay (background starts, rich toasts)", "appops set " + pkg + " SYSTEM_ALERT_WINDOW allow"},
		{"keep running in background", "cmd appops set " + pkg + " RUN_ANY_IN_BACKGROUND allow"},
		{"keep ADB on after reboots", "pm grant " + pkg + " android.permission.WRITE_SECURE_SETTINGS"},
	}
	for _, g := range grants {
		if out, err := conn.Shell(ctx, g.cmd); err != nil || strings.Contains(strings.ToLower(out), "error") {
			say("  %-42s failed: %s %v", g.what, out, err)
		} else {
			say("  %-42s ok", g.what)
		}
	}
	// Accessibility is a colon-separated list; append without clobbering
	// other services (Projectivy, screen readers).
	cur, _ := conn.Shell(ctx, "settings get secure enabled_accessibility_services")
	cur = strings.TrimSpace(cur)
	if cur == "null" {
		cur = ""
	}
	if !strings.Contains(cur, a11ySvc) {
		val := a11ySvc
		if cur != "" {
			val = cur + ":" + a11ySvc
		}
		if _, err := conn.Shell(ctx, "settings put secure enabled_accessibility_services "+val); err != nil {
			say("  %-42s failed: %v", "foreground/keyboard events, screenshots", err)
		} else {
			_, _ = conn.Shell(ctx, "settings put secure accessibility_enabled 1")
			say("  %-42s ok", "foreground/keyboard events, screenshots")
		}
	} else {
		say("  %-42s ok", "foreground/keyboard events, screenshots")
	}

	// 5. Start it. The service alone is enough and doesn't interrupt what's
	// on screen (an update shouldn't pause a movie). Starting it explicitly
	// also clears Android's "stopped" state so the boot broadcast arrives.
	_, _ = conn.Shell(ctx, "am start-foreground-service -n "+pkg+"/.ReceiverService")
	rc := receiver.New(host, "")
	say("Waiting for the receiver to come up …")
	for i := 0; i < 20 && !rc.Reachable(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !rc.Reachable() {
		// Fall back to the visible launch, then the TV's own API.
		if _, err := conn.Shell(ctx, "am start -n "+pkg+"/.MainActivity"); err != nil && o.Launch != nil {
			_ = o.Launch(ctx, pkg)
		}
		for i := 0; i < 20 && !rc.Reachable(); i++ {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !rc.Reachable() {
		return "", device.Failf("the companion app didn't start").Notef("nothing listening on :%d", receiver.Port)
	}

	// 6. Pair with it, same PIN dance as the TV itself, unless the stored
	// token still works.
	tok := existingToken
	if tok != "" {
		rc.Token = tok
		if _, err := rc.Playing(ctx); err != nil {
			tok = ""
		} else {
			say("Existing pairing still valid.")
		}
	}
	if tok == "" {
		rc.Token = ""
		t, err := rc.Pair(ctx, o.Ask)
		if err != nil {
			return "", err
		}
		tok = t
	}
	st, err := rc.Status(ctx)
	if err == nil {
		say("Receiver %s ready. now-playing:%v accessibility:%v screenshot:%v overlay:%v",
			st.Version, st.Caps["nowPlaying"], st.Caps["accessibility"], st.Caps["screenshot"], st.Caps["overlay"])
	}
	say("Done. You can turn ADB debugging back off; tvctl no longer needs it.")
	return tok, nil
}
