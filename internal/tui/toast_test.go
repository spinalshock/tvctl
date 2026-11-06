package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOverlayCentresAndKeepsSides(t *testing.T) {
	bg := strings.Repeat("abcdefghij\n", 4) + "abcdefghij"
	got := strings.Split(overlay(bg, "XX\nYY", 10), "\n")
	want := []string{"abcdefghij", "abcdXXghij", "abcdYYghij", "abcdefghij", "abcdefghij"}
	for i := range want {
		if ansi.Strip(got[i]) != want[i] {
			t.Fatalf("row %d = %q, want %q", i, ansi.Strip(got[i]), want[i])
		}
	}
}

func TestToastNewerOutlivesOlderTimer(t *testing.T) {
	m := &model{}
	m.showToast("launched com.netflix.ninja")
	old := m.toast.gen
	m.showToast("playing: x")
	m.toastDone(toastDoneMsg{gen: old})
	if m.toast.text != "playing: x" {
		t.Fatalf("older timer cleared the newer toast: %q", m.toast.text)
	}
	m.toastDone(toastDoneMsg{gen: m.toast.gen})
	if m.toast.text != "" {
		t.Fatal("toast not cleared")
	}
}
