package firetv

import "os"

// friendlyName is what the TV's pairing dialog calls us. Hostname is the
// most recognisable thing we have without asking.
func friendlyName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "tvctl"
	}
	return "tvctl on " + h
}
