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
// nothing. A Tailscale/CGNAT source means the host is reached over the
// tailnet, not this LAN. Otherwise, with no netmask to go by, sharing a
// /24 (or /64) with the source address is the best guess; home networks
// are /24.
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
	return localByRoute(ip, sourceFor(ip))
}

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
	if ip4, src4 := ip.To4(), src.To4(); ip4 != nil && src4 != nil {
		return ip4.Mask(net.CIDRMask(24, 32)).Equal(src4.Mask(net.CIDRMask(24, 32)))
	}
	return ip.Mask(net.CIDRMask(64, 128)).Equal(src.Mask(net.CIDRMask(64, 128)))
}
