package firetv

import (
	"context"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/omAthalye/tvctl/internal/netx"
)

// Found is a discovered Fire TV before pairing.
type Found struct {
	Host string
	Name string
}

// Discover finds Fire TVs on the LAN by listening to two things every Fire
// TV announces without any pairing:
//
//   - mDNS `_amzn-wplay._tcp`: Amazon's Whisperplay device manager. Its
//     instance name is an opaque id, so it tells us *where*, not *who*.
//   - mDNS `_airplay._tcp`: Fire TV Edition TVs with AirPlay advertise their
//     human name here ("Living Room Fire TV"). Macs advertise it too, so we
//     only use it to name hosts the first query already found.
//   - SSDP M-SEARCH for the DIAL service type, for networks where mDNS is
//     filtered but multicast UDP isn't.
//
// Every candidate is then confirmed by asking DIAL on :8009 about the
// FireTVRemote app, which only a Fire TV answers. timeout bounds the whole
// thing; two seconds finds everything on a normal home LAN.
func Discover(ctx context.Context, timeout time.Duration) ([]Found, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		mu    sync.Mutex
		hosts = map[string]string{} // ip -> name ("" until known)
	)
	add := func(ip, name string) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := hosts[ip]; !ok {
			hosts[ip] = ""
		}
		if name != "" {
			hosts[ip] = name
		}
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); browse(ctx, "_amzn-wplay._tcp", func(ip, _ string) { add(ip, "") }) }()
	go func() {
		defer wg.Done()
		// AirPlay names arrive for Macs too; remember them all, and only
		// attach the ones whose IP a Fire TV query also produced.
		names := map[string]string{}
		browse(ctx, "_airplay._tcp", func(ip, name string) { names[ip] = name })
		<-ctx.Done()
		mu.Lock()
		for ip, n := range names {
			if _, ok := hosts[ip]; ok {
				hosts[ip] = n
			}
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		netx.SSDP(ctx, "urn:dial-multiscreen-org:service:dial:1", func(ip string, _ textproto.MIMEHeader) { add(ip, "") })
	}()
	wg.Wait()

	// Confirm each candidate is actually a Fire TV. Short per-host budget
	// so one dead IP can't stall the list.
	var out []Found
	for ip, name := range hosts {
		pctx, pcancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		_, err := dialState(pctx, ip, "FireTVRemote")
		pcancel()
		if err != nil {
			continue
		}
		if name == "" {
			name = "Fire TV"
		}
		out = append(out, Found{Host: ip, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

// browse runs one mDNS query and reports every IPv4 it resolves. zeroconf
// delivers entries on a channel until ctx ends.
func browse(ctx context.Context, service string, hit func(ip, name string)) {
	r, err := zeroconf.NewResolver(nil)
	if err != nil {
		return
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	go func() {
		for e := range entries {
			for _, ip := range e.AddrIPv4 {
				hit(ip.String(), unescapeMDNS(e.Instance))
			}
		}
	}()
	_ = r.Browse(ctx, service, "local.", entries)
	<-ctx.Done()
}

// unescapeMDNS undoes the backslash escaping mDNS applies to instance names
// ("Living\ Room\ Fire\ TV" -> "Living Room Fire TV").
func unescapeMDNS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
