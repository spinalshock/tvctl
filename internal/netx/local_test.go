package netx

import (
	"errors"
	"net"
	"testing"
)

func TestLocalByRoute(t *testing.T) {
	ip := func(s string) net.IP { return net.ParseIP(s) }
	cases := []struct {
		name     string
		host     string
		src      net.IP
		wantHere bool
	}{
		{"no route: can't tell, keep the default", "192.168.0.106", nil, true},
		{"reached over the tailnet", "192.168.0.106", ip("100.101.102.103"), false},
		{"same /24 as the LAN address", "192.168.29.76", ip("192.168.29.197"), true},
		{"other home's LAN, no VPN up", "192.168.0.106", ip("192.168.29.197"), false},
		{"ipv6 same /64", "fd00:1:2:3::10", ip("fd00:1:2:3::1"), true},
		{"ipv6 other /64", "fd00:1:2:4::10", ip("fd00:1:2:3::1"), false},
	}
	for _, c := range cases {
		if got := localByRoute(ip(c.host), c.src); got != c.wantHere {
			t.Errorf("%s: localByRoute(%s, %v) = %v, want %v", c.name, c.host, c.src, got, c.wantHere)
		}
	}
}

// Android's failure mode: the interface list is refused, so the route
// source decides.
func TestOnLocalNetWithoutInterfaceList(t *testing.T) {
	savedAddrs, savedSrc := interfaceAddrs, sourceFor
	defer func() { interfaceAddrs, sourceFor = savedAddrs, savedSrc }()
	interfaceAddrs = func() ([]net.Addr, error) { return nil, errors.New("route ip+net: netlinkrib: permission denied") }
	sourceFor = func(ip net.IP) net.IP {
		if ip.String() == "192.168.29.76" {
			return net.ParseIP("192.168.29.197") // on the Wi-Fi
		}
		return net.ParseIP("100.101.102.103") // via the subnet router
	}
	if !OnLocalNet("192.168.29.76") {
		t.Error("the TV on this Wi-Fi should count as local")
	}
	if OnLocalNet("192.168.0.106") {
		t.Error("the TV behind the subnet router should not count as local")
	}
	if !OnLocalNet("living-room-tv") {
		t.Error("a hostname counts as local")
	}
}

func TestOnLocalNetUsesInterfaceList(t *testing.T) {
	savedAddrs, savedSrc := interfaceAddrs, sourceFor
	defer func() { interfaceAddrs, sourceFor = savedAddrs, savedSrc }()
	_, lan, _ := net.ParseCIDR("10.0.0.5/16")
	interfaceAddrs = func() ([]net.Addr, error) { return []net.Addr{lan}, nil }
	sourceFor = func(net.IP) net.IP {
		t.Fatal("route source consulted although the interface list was readable")
		return nil
	}
	if !OnLocalNet("10.0.7.9") || OnLocalNet("192.168.0.106") {
		t.Error("the interface list decides when it is readable")
	}
}
