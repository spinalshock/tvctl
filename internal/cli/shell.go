package cli

import (
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/adb"
	"github.com/omAthalye/tvctl/internal/config"
)

// shell runs one command on the TV over ADB with tvctl's own key, the one
// the TV trusted during setup. Hidden from help: it is a maintenance tool
// for people who know what they're doing (debloating, inspecting memory),
// not part of the everyday surface. Needs ADB debugging on, like setup.
func (e *cmds) shell() *cobra.Command {
	return &cobra.Command{
		Use:    "shell <command>...",
		Short:  "Run a shell command on the TV over ADB (needs ADB debugging on)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			cfgPath, err := config.Path()
			if err != nil {
				return err
			}
			key, err := adb.LoadOrCreateKey(filepath.Join(filepath.Dir(cfgPath), "adbkey"))
			if err != nil {
				return err
			}
			conn, err := adb.Dial(s.ctx, net.JoinHostPort(s.entry.Host, "5555"), key, func() {
				e.emit("The TV is asking \"Allow USB debugging?\" — choose Always allow.", nil)
			}, 60*time.Second)
			if err != nil {
				return err
			}
			defer conn.Close()
			out, err := conn.Shell(s.ctx, strings.Join(args, " "))
			if err != nil {
				return err
			}
			e.emit(strings.TrimRight(out, "\n"), map[string]any{"output": out})
			return nil
		}),
	}
}
