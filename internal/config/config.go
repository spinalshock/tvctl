// Package config persists paired devices. One JSON file, read fully and
// written atomically, because the data is tiny and a corrupt half-written
// file would cost the user a re-pair.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
)

// Entry is one paired TV. Token is whatever the driver's Pair returned and is
// opaque here. MAC is optional and only used for Wake-on-LAN.
type Entry struct {
	Name  string `json:"name"`
	Brand string `json:"brand"`
	Host  string `json:"host"`
	MAC   string `json:"mac,omitempty"`
	Token string `json:"token,omitempty"`
	// CompanionToken is the companion app's pairing token (tv companion).
	// The JSON key predates the name.
	CompanionToken string `json:"receiver_token,omitempty"`
}

// Store is the on-disk document. Default names the entry used when the user
// doesn't pass --device, so a one-TV household never has to.
type Store struct {
	Default string           `json:"default,omitempty"`
	Devices map[string]Entry `json:"devices"`
	// Pending is a pairing waiting for its PIN: `tv pair` showed it on
	// the TV, `tv pin` will send it back. Nothing stays open in between.
	Pending *Pending `json:"pending,omitempty"`
	path    string
}

// Pending is the TV whose PIN is on screen, and when it was shown; PINs
// expire, so a stale entry is discarded rather than tried.
type Pending struct {
	Brand   string    `json:"brand"`
	Name    string    `json:"name"`
	Host    string    `json:"host"`
	Started time.Time `json:"started"`
}

// Path follows XDG on macOS and Linux (~/.config/tvctl) rather than Go's
// os.UserConfigDir, which on macOS points into ~/Library and is where nobody
// looks for a CLI's config. Windows keeps the platform convention.
func Path() (string, error) {
	if p := os.Getenv("TVCTL_CONFIG"); p != "" {
		return p, nil
	}
	var base string
	if runtime.GOOS == "windows" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = d
	} else if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		base = x
	} else {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(h, ".config")
	}
	return filepath.Join(base, "tvctl", "config.json"), nil
}

// Load returns an empty store (not an error) when the file doesn't exist yet,
// so first-run code paths don't need a special case.
func Load() (*Store, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	s := &Store{Devices: map[string]Entry{}, path: p}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, device.Failf("the config file is damaged").Note(p).Because(err)
	}
	if s.Devices == nil {
		s.Devices = map[string]Entry{}
	}
	return s, nil
}

// Save writes to a temp file in the same directory and renames it over the
// old one. Rename is atomic on every OS we care about, so readers see either
// the old or the new document, never a truncated one. Mode 0600 because the
// file holds pairing tokens.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Put adds or replaces a device and makes it the default when it is the only
// one, which is the common case.
func (s *Store) Put(id string, e Entry) {
	s.Devices[id] = e
	if s.Default == "" || len(s.Devices) == 1 {
		s.Default = id
	}
}

// Resolve picks the device for a command: an explicit id or name, else the
// default. Name matching is exact and case-sensitive on purpose; fuzzy
// matching belongs in the UI layer where it can show its guess.
func (s *Store) Resolve(sel string) (string, Entry, error) {
	if sel == "" {
		sel = s.Default
		// Paired TVs in different homes: when the default isn't on any
		// network this machine is on, use the one that is.
		if e, ok := s.Devices[sel]; ok && !onLocalNet(e.Host) {
			ids := make([]string, 0, len(s.Devices))
			for id, d := range s.Devices {
				if onLocalNet(d.Host) {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				sort.Strings(ids) // several here: the same one every time
				return ids[0], s.Devices[ids[0]], nil
			}
		}
	}
	if sel == "" {
		return "", Entry{}, device.Fail("no TV paired yet").Note("run tv pair")
	}
	if e, ok := s.Devices[sel]; ok {
		return sel, e, nil
	}
	for id, e := range s.Devices {
		if e.Name == sel || e.Host == sel {
			return id, e, nil
		}
	}
	return "", Entry{}, device.Failf("no TV called %s", sel)
}

// onLocalNet is netx.OnLocalNet, a variable so tests can pick the network.
var onLocalNet = netx.OnLocalNet
