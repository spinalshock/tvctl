//go:build lite

package main

import "errors"

// The lite build has no interactive prompt: it is driven one command at a
// time by a script or an assistant. Build without -tags lite for the TUI.
func openTUI() error {
	return errors.New("this build has no interactive mode: give a command, or see tv help")
}
