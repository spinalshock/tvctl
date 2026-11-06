package firetv

import (
	"testing"

	"github.com/omAthalye/tvctl/internal/device"
)

// Every key in the shared vocabulary either maps to a Fire TV action or is
// a documented gap; a new key added to device.AllKeys must land in one of
// the two, so the CLI's completion never offers a key the driver rejects
// by accident.
func TestKeysCovered(t *testing.T) {
	handledInKey := map[device.Key]bool{device.KeyPlayPause: true, device.KeyFastFwd: true, device.KeyRewind: true}
	gaps := map[device.Key]bool{device.KeyStop: true} // no stop action on the media route; the companion app covers it
	for _, k := range device.AllKeys {
		_, mapped := keyActions[k]
		switch {
		case mapped || handledInKey[k]:
			if gaps[k] {
				t.Errorf("%s is supported now; drop it from gaps", k)
			}
		case gaps[k]:
		default:
			t.Errorf("%s is in device.AllKeys but Fire TV neither maps nor documents it", k)
		}
	}
}
