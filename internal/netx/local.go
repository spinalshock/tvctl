package netx

import "net"

// OnLocalNet reports whether host is on a network this machine is
// directly on, as opposed to one it reaches through a VPN or a subnet
// router. A hostname, or a machine that can't tell, counts as local, so
// callers only ever steer away from a TV that clearly isn't here.
//
// The interface list is the honest answer. Android refuses it to apps
// (netlink: permission denied, Termux included), so there the kernel is
// asked which source address it would use for host; a UDP "dial" sends
// nothing. With no netmask to go by, sharing a /24 (or /64) with the
// machine's own address is the best guess; home networks are /24.
//
// Two source addresses are consulted, because a tailnet can claim the
// LAN itself: when a subnet router advertises this very network, even
// the TV next door is routed into the tunnel and its source is the
// Tailscale address. So first the source for the default route (an
// address nothing advertises) gives the LAN address, and a TV in its /24
// is local. Then the host's own source decides: Tailscale/CGNAT means it
// is reached over the tailnet, not from here.
func OnLocalNet(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	if addrs, err := interfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if lan := sourceFor(defaultRouteProbe); lan != nil && !cgnat.Contains(lan) && samePrefix(ip, lan) {
		return true
	}
	return localByRoute(ip, sourceFor(ip))
}

// defaultRouteProbe is TEST-NET-1 (RFC 5737): never assigned, never
// advertised by a subnet router, so the kernel answers with the default
// route's source address, the LAN one. Nothing is sent to it.
var defaultRouteProbe = net.IPv4(192, 0, 2, 1)

// Swapped out by tests.
var (
	interfaceAddrs = net.InterfaceAddrs
	sourceFor      = routeSource
)

// routeSource is the local address the kernel would send from to reach
// ip, or nil when it has no route. No packet leaves the machine.
func routeSource(ip net.IP) net.IP {
	c, err := net.Dial("udp", net.JoinHostPort(ip.String(), "9"))
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// cgnat is 100.64.0.0/10, the range Tailscale (and carrier NAT) hands out.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// localByRoute decides from the source address alone: see OnLocalNet.
func localByRoute(ip, src net.IP) bool {
	if src == nil {
		return true
	}
	if cgnat.Contains(src) {
		return false
	}
	return samePrefix(ip, src)
}

// samePrefix is the /24 (IPv4) or /64 (IPv6) guess at "same network".
func samePrefix(a, b net.IP) bool {
	if a4, b4 := a.To4(), b.To4(); a4 != nil && b4 != nil {
		return a4.Mask(net.CIDRMask(24, 32)).Equal(b4.Mask(net.CIDRMask(24, 32)))
	}
	return a.Mask(net.CIDRMask(64, 128)).Equal(b.Mask(net.CIDRMask(64, 128)))
}
