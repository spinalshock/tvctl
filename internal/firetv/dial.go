package firetv

import (
	"context"
	"fmt"
	"net"

	"github.com/spinalshock/tvctl/internal/dial"
)

// Fire TV runs DIAL on a fixed port regardless of pairing. We use it for
// two things:
//
//  1. Launching the remote-control service. The HTTPS API on 8080 is not
//     listening until something starts it, and it stops itself again after a
//     few minutes idle. POST /apps/FireTVRemote is the kick the phone app
//     sends. No auth, works while the TV is asleep.
//  2. Launching DIAL-registered apps (YouTube, Netflix) with a payload, which
//     is the no-ADB way to open a URL in an app.

const dialPort = 8009

// dialApps is the base URL every DIAL app hangs off.
func dialApps(host string) string {
	return fmt.Sprintf("http://%s/apps", net.JoinHostPort(host, fmt.Sprint(dialPort)))
}

func dialLaunch(ctx context.Context, host, app, body string) error {
	return dial.Launch(ctx, dialApps(host), app, body, nil)
}

func dialLaunchWith(ctx context.Context, host, app, body string, hdr map[string]string) error {
	return dial.Launch(ctx, dialApps(host), app, body, hdr)
}

// dialState is a cheap liveness probe: any answer on the DIAL port means
// the TV is awake enough to talk.
func dialState(ctx context.Context, host, app string) (string, error) {
	return dial.State(ctx, dialApps(host), app, nil)
}
