// Package netx holds the one network probe every brand and the companion
// app client need: "is anything listening there right now?".
package netx

import (
	"net"
	"time"
)

// PortOpen reports whether a TCP connection to addr succeeds within 700ms.
// That is long enough for a TV on the LAN or across a tailnet, short
// enough that a passive poll never feels stuck.
func PortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
