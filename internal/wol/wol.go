// Package wol sends Wake-on-LAN magic packets. Every brand that can be woken
// over the network shares this one implementation.
package wol

import (
	"bytes"
	"github.com/spinalshock/tvctl/internal/device"
	"net"
)

// Send broadcasts a magic packet: six 0xFF bytes followed by the target MAC
// repeated sixteen times, to UDP port 9 on the broadcast address. The TV's
// NIC pattern-matches this in standby and powers the SoC. It only works when
// the owner enabled network wake on the TV ("Wake on LAN" on Fire TV, "Turn
// on via Wi-Fi" on LG); it is harmless when ignored, so callers send it
// unconditionally.
func Send(mac string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return device.Failf("%s is not a MAC address", mac).Because(err)
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
