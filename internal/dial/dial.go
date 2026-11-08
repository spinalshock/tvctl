// Package dial speaks DIAL (DIscovery And Launch), the plain-HTTP app
// launcher most smart TVs run beside their own API: Fire TV on a fixed
// port, LG on a port the TV announces over SSDP. It is how an app is
// started with a payload without the brand's API being involved, and
// the first step of YouTube's casting flow (see internal/youtube).
//
// An "apps URL" is the base every app hangs off, "http://host:port/apps";
// a brand either knows it (Fire TV) or asks Find for it.
package dial

import (
	"context"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
	"github.com/spinalshock/tvctl/internal/trace"
)

// ServiceType is the SSDP search target every DIAL server answers.
const ServiceType = "urn:dial-multiscreen-org:service:dial:1"

// httpc is for the unencrypted DIAL side. Short timeout: the TV is on the
// LAN, and anything slower than this means it's asleep, not busy.
var httpc = &http.Client{Timeout: 5 * time.Second}

// Launch POSTs body to apps/app, with extra headers for apps that gate
// their endpoint on an Origin (YouTube). 200 and 201 both mean running.
func Launch(ctx context.Context, apps, app, body string, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apps+"/"+app, strings.NewReader(body))
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
	resp, err := httpc.Do(req)
	if err != nil {
		return device.Fail("couldn't reach the TV's app service").Because(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return device.Failf("the TV wouldn't start %s", app).Notef("HTTP %d", resp.StatusCode)
	}
	return nil
}

// State GETs an app's state ("running"/"stopped"). Any answer at all also
// proves the TV is awake enough to talk, which Fire TV discovery uses.
func State(ctx context.Context, apps, app string, hdr map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apps+"/"+app, nil)
	if err != nil {
		return "", err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return "", device.Fail("the TV's app service refused").Notef("HTTP %d", resp.StatusCode)
	}
	return stateIn(string(b)), nil
}

func stateIn(xml string) string {
	if i := strings.Index(xml, "<state>"); i >= 0 {
		xml = xml[i+len("<state>"):]
		if j := strings.Index(xml, "</state>"); j >= 0 {
			return xml[:j]
		}
	}
	return ""
}

// Find asks host for its DIAL apps URL: an SSDP search answered with the
// device description's location, whose response carries the
// Application-URL header. The host is asked directly and by multicast at
// the same time, first answer wins: a TV reached through a tunnel never
// answers the direct ask, and Android sometimes can't multicast, so each
// covers the other without costing a wait. Bounded by timeout.
func Find(ctx context.Context, host string, timeout time.Duration) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		mu  sync.Mutex
		loc string
		how string
	)
	hit := func(via string) func(ip string, h textproto.MIMEHeader) {
		return func(ip string, h textproto.MIMEHeader) {
			if ip != host || h.Get("Location") == "" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if loc == "" {
				loc, how = h.Get("Location"), via
				cancel() // one answer is all that's needed
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); netx.SSDPAt(sctx, host, ServiceType, hit("direct")) }()
	go func() { defer wg.Done(); netx.SSDP(sctx, ServiceType, hit("multicast")) }()
	wg.Wait()
	trace.Logf("dial: SSDP answered=%v via %s", loc != "", how)
	if loc == "" {
		return "", device.Fail("the TV doesn't offer app casting").Note("no DIAL service answered")
	}
	apps, err := appsFrom(ctx, loc)
	trace.Logf("dial: apps URL %q err=%v", apps, err)
	return apps, err
}

// appsFrom reads the Application-URL header off a device description.
func appsFrom(ctx context.Context, location string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", device.Fail("couldn't read the TV's casting details").Because(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	apps := strings.TrimSuffix(resp.Header.Get("Application-URL"), "/")
	if apps == "" {
		return "", device.Fail("the TV doesn't offer app casting").Note("description has no Application-URL")
	}
	return apps, nil
}
