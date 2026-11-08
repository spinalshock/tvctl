package netx

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestFixResolver(t *testing.T) {
	saved := net.DefaultResolver
	defer func() { net.DefaultResolver = saved }()
	dir := t.TempDir()
	system := filepath.Join(dir, "resolv.conf")
	termux := filepath.Join(dir, "termux-resolv.conf")
	_ = os.WriteFile(termux, []byte("# Termux\nnameserver 8.8.8.8\nnameserver 2001:4860:4860::8888\nsearch lan\nnameserver not-an-ip\n"), 0o644)

	if got := fixResolver(termux, termux); got != nil { // a system file exists: leave Go alone
		t.Errorf("system resolv.conf present, but resolver replaced with %v", got)
	}
	got := fixResolver(system, termux) // no system file: Termux's servers
	want := []string{"8.8.8.8:53", "[2001:4860:4860::8888]:53"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("servers = %v, want %v", got, want)
	}
	if net.DefaultResolver == saved || !net.DefaultResolver.PreferGo || net.DefaultResolver.Dial == nil {
		t.Error("the default resolver was not replaced with a Go resolver dialing those servers")
	}
	got = fixResolver(system, filepath.Join(dir, "missing")) // nothing anywhere: public resolvers
	if len(got) != 2 || got[0] != "8.8.8.8:53" {
		t.Errorf("fallback servers = %v", got)
	}
}
