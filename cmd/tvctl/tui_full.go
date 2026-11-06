//go:build !lite

package main

import "github.com/omAthalye/tvctl/internal/tui"

// openTUI starts the interactive prompt. The lite build (an always-on
// host driven by an assistant) leaves it out; see tui_lite.go.
func openTUI() error { return tui.Run() }
