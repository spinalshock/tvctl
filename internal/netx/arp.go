package netx

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var reMAC = regexp.MustCompile(`(?i)\b([0-9a-f]{1,2}(?::[0-9a-f]{1,2}){5})\b`)

// MACOf returns the hardware address the local machine has cached for ip,
// or "" when it has none. It only works for a host on the same LAN that
// we talked to a moment ago, which is exactly when a TV's MAC is wanted
// for Wake-on-LAN. Linux is read from /proc/net/arp; everything else asks
// the arp command. Both are best effort.
func MACOf(ip string) string {
	if f, err := os.Open("/proc/net/arp"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 4 && fields[0] == ip {
				return normMAC(fields[3])
			}
		}
	}
	out, err := exec.Command("arp", "-n", ip).Output()
	if err != nil {
		return ""
	}
	return normMAC(string(out))
}

// normMAC pulls the first MAC out of s, zero-pads octets (macOS prints
// "4:9:86:..."), lowercases it, and rejects the empty and broadcast forms.
func normMAC(s string) string {
	m := reMAC.FindString(s)
	if m == "" {
		return ""
	}
	parts := strings.Split(strings.ToLower(m), ":")
	for i, p := range parts {
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	mac := strings.Join(parts, ":")
	if mac == "00:00:00:00:00:00" || mac == "ff:ff:ff:ff:ff:ff" {
		return ""
	}
	return mac
}
