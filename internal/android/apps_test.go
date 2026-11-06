package android

import "testing"

func TestPackagesAndNames(t *testing.T) {
	if p, ok := Package("YouTube"); !ok || p != "org.smarttube.stable" {
		t.Errorf("YouTube -> %q %v", p, ok)
	}
	if _, ok := Package("Nope"); ok {
		t.Error("unknown app resolved")
	}
	if AppName("com.netflix.ninja") != "Netflix" || AppName("x.y") != "x.y" {
		t.Error("AppName")
	}
	// Every package in the forward table has a display name.
	for name, pkg := range packages {
		if AppName(pkg) == pkg {
			t.Errorf("%s (%s) has no display name", name, pkg)
		}
	}
}
