package cli

import (
	"fmt"
	"net"
	"os"
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
// adbConn dials the TV's ADB with tvctl's trusted key.
func (e *cmds) adbConn(s *session) (*adb.Conn, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return nil, err
	}
	key, err := adb.LoadOrCreateKey(filepath.Join(filepath.Dir(cfgPath), "adbkey"))
	if err != nil {
		return nil, err
	}
	return adb.Dial(s.ctx, net.JoinHostPort(s.entry.Host, "5555"), key, func() {
		e.emit("The TV is asking \"Allow USB debugging?\" — choose Always allow.", nil)
	}, 60*time.Second)
}

func (e *cmds) shell() *cobra.Command {
	return &cobra.Command{
		Use:    "shell <command>...",
		Short:  "Run a shell command on the TV over ADB (needs ADB debugging on)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			conn, err := e.adbConn(s)
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

// install sideloads an APK over ADB (installs or updates in place). Hidden,
// like shell: a maintenance tool. Needs ADB debugging on.
func (e *cmds) install() *cobra.Command {
	return &cobra.Command{
		Use:    "install <file.apk>",
		Short:  "Install or update an APK on the TV over ADB (needs ADB debugging on)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			apk, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			conn, err := e.adbConn(s)
			if err != nil {
				return err
			}
			defer conn.Close()
			out, err := conn.Install(s.ctx, apk, nil)
			if err != nil {
				return err
			}
			out = strings.TrimSpace(out)
			if !strings.Contains(out, "Success") {
				return fmt.Errorf("install failed: %s", out)
			}
			return e.ok(fmt.Sprintf("installed %s (%.1f MB)", filepath.Base(args[0]), float64(len(apk))/1e6), map[string]any{"apk": args[0]})
		}),
	}
}
