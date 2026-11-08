package netx

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Android has no /etc/resolv.conf, so Go's own resolver falls back to
// [::1]:53, where nothing listens, and every hostname lookup fails with
// "connection refused" while LAN traffic by IP works fine. Termux keeps
// its own resolv.conf under $PREFIX; failing that, public resolvers are
// better than none. Only the system resolver's address changes; Go
// still does the lookups. Hosts with /etc/resolv.conf are untouched.
func init() { fixResolver("/etc/resolv.conf", termuxResolvConf()) }

func termuxResolvConf() string {
	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = "/data/data/com.termux/files/usr"
	}
	return filepath.Join(prefix, "etc", "resolv.conf")
}

// fixResolver installs a resolver that asks the given servers when the
// system file is missing. Returns the servers it chose, "" when the
// system resolver was left alone.
func fixResolver(systemConf, altConf string) []string {
	if _, err := os.Stat(systemConf); err == nil {
		return nil
	}
	servers := nameservers(altConf)
	if len(servers) == 0 {
		servers = []string{"8.8.8.8:53", "1.1.1.1:53"}
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			var last error
			for _, s := range servers {
				c, err := d.DialContext(ctx, network, s)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return servers
}

// nameservers reads the "nameserver" lines of a resolv.conf as host:port.
func nameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if ip := net.ParseIP(fields[1]); ip != nil {
				out = append(out, net.JoinHostPort(ip.String(), "53"))
			}
		}
	}
	return out
}
