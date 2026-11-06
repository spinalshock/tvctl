//go:build lite

package cli

import "github.com/spf13/cobra"

// The ADB verbs (shell, install) need the adb package; the lite build
// leaves them out. nil commands are skipped when the tree is built.
func (e *cmds) shell() *cobra.Command   { return nil }
func (e *cmds) install() *cobra.Command { return nil }
