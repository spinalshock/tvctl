// Package webos drives LG webOS TVs.
package webos

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spinalshock/tvctl/internal/netx"
)

// Found is a discovered LG TV before pairing.
type Found struct {
	Host  string
	Name  string // "webOS TV UR8040PSB", as the TV names itself
	Model string // "43UR8040PSB"
}

// secondScreen is the SSDP service type every webOS TV answers on; it is
// the one LG's own phone app searches for.
const secondScreen = "urn:lge-com:service:webos-second-screen:1"

// Discover finds LG TVs on the LAN with one SSDP search for the webOS
// second-screen service, then reads each responder's UPnP description for
// its name and model. timeout bounds the search.
func Discover(ctx context.Context, timeout time.Duration) ([]Found, error) {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locs := map[string]string{} // ip -> description URL
	var mu sync.Mutex
	netx.SSDP(sctx, secondScreen, func(ip string, h textproto.MIMEHeader) {
		if !strings.Contains(h.Get("ST"), "webos-second-screen") {
			return
		}
		mu.Lock()
		locs[ip] = h.Get("Location")
		mu.Unlock()
	})

	var out []Found
	for ip, loc := range locs {
		f := Found{Host: ip, Name: "LG TV"}
		dctx, dcancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		if d, err := describe(dctx, loc); err == nil {
			if d.Name != "" {
				f.Name = strings.TrimSpace(strings.TrimPrefix(d.Name, "[LG]"))
			}
			f.Model = d.model()
		}
		dcancel()
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

type description struct {
	Name   string `xml:"device>friendlyName"`
	Model  string `xml:"device>modelName"`
	Number string `xml:"device>modelNumber"`
}

// model is the real model ("43UR8040PSB"): LG puts it in modelName in some
// descriptions and in modelNumber in others, beside a generic "LG Smart TV".
func (d *description) model() string {
	for _, m := range []string{d.Model, d.Number} {
		if !strings.ContainsAny(m, " .") && strings.ContainsAny(m, "0123456789") {
			return m
		}
	}
	return ""
}

// describe reads a UPnP device description.
func describe(ctx context.Context, loc string) (*description, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var d description
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}
