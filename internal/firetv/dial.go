package firetv

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DIAL (DIscovery And Launch) is a plain-HTTP protocol on port 8009 that
// every Fire TV runs regardless of pairing. We use it for two things:
//
//  1. Launching the remote-control service. The HTTPS API on 8080 is not
//     listening until something starts it, and it stops itself again after a
//     few minutes idle. POST /apps/FireTVRemote is the kick the phone app
//     sends. No auth, works while the TV is asleep.
//  2. Launching DIAL-registered apps (Netflix, Silk) with a payload, which is
//     the no-ADB way to open a URL in an app.

const dialPort = 8009

// dialURL builds http://host:8009/apps/<name>.
func dialURL(host, app string) string {
	return fmt.Sprintf("http://%s/apps/%s", net.JoinHostPort(host, fmt.Sprint(dialPort)), app)
}

// dialLaunch POSTs to a DIAL app endpoint. body is the app-specific payload
// (empty for FireTVRemote). 200 and 201 both mean "running".
func dialLaunch(ctx context.Context, host, app, body string) error {
	return dialLaunchWith(ctx, host, app, body, nil)
}

// dialLaunchWith is dialLaunch with extra headers, for apps that gate their
// DIAL endpoint on an Origin (YouTube).
func dialLaunchWith(ctx context.Context, host, app, body string, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dialURL(host, app), strings.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	// DIAL servers are picky: an explicit Content-Length even for an empty
	// body, and text/plain for payloads.
	req.ContentLength = int64(len(body))
	if body != "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	resp, err := plainHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("dial %s: %w", app, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("dial %s: HTTP %d", app, resp.StatusCode)
	}
	return nil
}

// dialState GETs a DIAL app's state XML ("running"/"stopped") and is a cheap
// liveness probe for the TV itself: any answer on 8009 means it's awake
// enough to talk.
func dialState(ctx context.Context, host, app string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dialURL(host, app), nil)
	if err != nil {
		return "", err
	}
	resp, err := plainHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	s := string(b)
	if i := strings.Index(s, "<state>"); i >= 0 {
		s = s[i+len("<state>"):]
		if j := strings.Index(s, "</state>"); j >= 0 {
			return s[:j], nil
		}
	}
	return "", nil
}

// plainHTTP is for the unencrypted DIAL side. Short timeouts: the TV is on
// the LAN, and anything slower than this means it's asleep, not busy.
var plainHTTP = &http.Client{Timeout: 5 * time.Second}
