package webos

import (
	"testing"

	"github.com/spinalshock/tvctl/internal/device"
)

// Every key in the shared vocabulary reaches the TV one way or another.
func TestKeysCovered(t *testing.T) {
	handledInKey := map[device.Key]bool{device.KeyPlayPause: true, device.KeySleep: true, device.KeyWake: true}
	for _, k := range device.AllKeys {
		if _, ok := buttons[k]; !ok && !handledInKey[k] {
			t.Errorf("%s is in device.AllKeys but webOS has no button for it", k)
		}
	}
}
