// Package android holds what is true of every Android-based TV: the
// package behind each app people name, and the name behind each package.
// Fire TV is one such TV; the companion app is itself an Android app and
// reports package names, so its client needs the same table. A future
// Android TV driver reuses it unchanged.
package android

import "strings"

// packages maps a well-known app's name (lowercase) to its package.
// YouTube is SmartTube, an ad-free client, by tvctl's choice; the official
// app is "com.amazon.firetv.youtube".
var packages = map[string]string{
	"netflix":     "com.netflix.ninja",
	"youtube":     "org.smarttube.stable",
	"spotify":     "com.spotify.tv.android",
	"prime video": "com.amazon.firebat",
	"silk":        "com.amazon.cloud9",
	"browser":     "com.amazon.cloud9",
	"geforce now": "com.nvidia.tegrazone3",
	"projectivy":  "com.spocky.projengmenu",
}

var names = map[string]string{
	"com.netflix.ninja":                 "Netflix",
	"org.smarttube.stable":              "YouTube",
	"com.amazon.firetv.youtube":         "YouTube",
	"com.spotify.tv.android":            "Spotify",
	"com.amazon.firebat":                "Prime Video",
	"com.amazon.cloud9":                 "Silk",
	"com.amazon.venezia":                "Appstore",
	"com.nvidia.tegrazone3":             "GeForce NOW",
	"com.spocky.projengmenu":            "Projectivy",
	"com.amazon.tv.launcher":            "Fire TV Home",
	"dev.tvctl.receiver":                "tvctl",
	"com.onemainstream.sonyliv.android": "Sony LIV",
}

// Package is the package for a well-known app name, if it is one.
func Package(name string) (string, bool) {
	p, ok := packages[strings.ToLower(name)]
	return p, ok
}

// AppName is the name people know a package by; the package itself when
// it isn't a well-known one, so it is always safe to display.
func AppName(pkg string) string {
	if n, ok := names[pkg]; ok {
		return n
	}
	return pkg
}
