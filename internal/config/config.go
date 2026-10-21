// Package config persists paired devices. One JSON file, read fully and
// written atomically, because the data is tiny and a corrupt half-written
// file would cost the user a re-pair.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Entry is one paired TV. Token is whatever the driver's Pair returned and is
// opaque here. MAC is optional and only used for Wake-on-LAN.
type Entry struct {
	Name  string `json:"name"`
	Brand string `json:"brand"`
	Host  string `json:"host"`
	MAC   string `json:"mac,omitempty"`
	Token string `json:"token,omitempty"`
}

// Store is the on-disk document. Default names the entry used when the user
// doesn't pass --device, so a one-TV household never has to.
type Store struct {
	Default string           `json:"default,omitempty"`
	Devices map[string]Entry `json:"devices"`
	path    string
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
		return nil, fmt.Errorf("parse %s: %w", p, err)
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
	}
	if sel == "" {
		return "", Entry{}, errors.New("no device paired yet: run `tv pair`")
	}
	if e, ok := s.Devices[sel]; ok {
		return sel, e, nil
	}
	for id, e := range s.Devices {
		if e.Name == sel || e.Host == sel {
			return id, e, nil
		}
	}
	return "", Entry{}, fmt.Errorf("unknown device %q", sel)
}
