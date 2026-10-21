package firetv

import (
	"bytes"
	"fmt"
	"net"
	"os"
)

// sendMagicPacket broadcasts a Wake-on-LAN frame: six 0xFF bytes followed by
// the target MAC repeated sixteen times, sent to UDP port 9 on the broadcast
// address. The TV's NIC pattern-matches this in standby and powers the SoC.
// Only works if the user turned on "Wake on LAN" in the TV settings; we send
// it unconditionally because it's harmless when ignored.
func sendMagicPacket(mac string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("bad MAC %q: %w", mac, err)
	}
	var buf bytes.Buffer
	buf.Write(bytes.Repeat([]byte{0xFF}, 6))
	buf.Write(bytes.Repeat(hw, 16))
	conn, err := net.Dial("udp", "255.255.255.255:9")
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(buf.Bytes())
	return err
}

// friendlyName is what the TV's pairing dialog calls us. Hostname is the
// most recognisable thing we have without asking.
func friendlyName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "tvctl"
	}
	return "tvctl on " + h
}
