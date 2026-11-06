package netx

import (
	"bufio"
	"context"
	"net"
	"net/textproto"
	"strings"
	"time"
)

// SSDP sends a multicast M-SEARCH for the service type st and calls hit
// for every 200 response until ctx ends, with the responder's IP and its
// headers (LOCATION, ST, USN, …). Every brand that advertises over UPnP is
// found this way; only st and what the caller reads from the headers
// differ. Best effort: a socket failure finds nothing.
func SSDP(ctx context.Context, st string, hit func(ip string, h textproto.MIMEHeader)) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() { // Ctrl-C ends the read, not just the deadline
		select {
		case <-ctx.Done():
			_ = conn.SetReadDeadline(time.Now())
		case <-done:
		}
	}()
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n" +
		"ST: " + st + "\r\n\r\n"
	for i := 0; i < 2; i++ { // UDP; send twice for luck
		_, _ = conn.WriteTo([]byte(msg), dst)
	}
	buf := make([]byte, 2048)
	for {
		dl, _ := ctx.Deadline()
		_ = conn.SetReadDeadline(dl)
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		r := textproto.NewReader(bufio.NewReader(strings.NewReader(string(buf[:n]))))
		status, err := r.ReadLine()
		if err != nil || !strings.HasPrefix(status, "HTTP/1.1 200") {
			continue
		}
		h, _ := r.ReadMIMEHeader()
		if ua, ok := from.(*net.UDPAddr); ok {
			hit(ua.IP.String(), h)
		}
	}
}
