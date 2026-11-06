package netx

import "testing"

func TestNormMAC(t *testing.T) {
	cases := map[string]string{
		"? (192.168.29.76) at 4:9:86:ab:cd:ef on en0 ifscope [ethernet]": "04:09:86:ab:cd:ef",
		"64:E4:A5:E5:4A:AC":            "64:e4:a5:e5:4a:ac",
		"? (10.0.0.9) at (incomplete)": "",
		"00:00:00:00:00:00":            "",
	}
	for in, want := range cases {
		if got := normMAC(in); got != want {
			t.Errorf("normMAC(%q) = %q, want %q", in, got, want)
		}
	}
}
