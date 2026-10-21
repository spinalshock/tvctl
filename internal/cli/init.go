package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// init emits shell code that defines a short command, `tv` by default,
// in front of the real binary, the way zoxide's `z` fronts `zoxide`. The
// package keeps a unique, registry-safe name; what people type is theirs
// to choose, and it never collides with anything published anywhere.
//
// The function is a function, not an alias, so it can open the TUI when
// called bare and pass everything else through. Completion is pointed at
// cobra's generated completer for the real binary.
func (e *cmds) initCmd() *cobra.Command {
	var alias string
	c := &cobra.Command{
		Use:   "init zsh|bash|fish",
		Short: "Print shell code that defines the short command (default: tv)",
		Long: `Add to your shell config:

  zsh:   eval "$(tvctl init zsh)"
  bash:  eval "$(tvctl init bash)"
  fish:  tvctl init fish | source

Then type  tv  for the TUI and  tv <command>  for everything else. Use
--alias to pick another word if tv is taken on your machine.`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"zsh", "bash", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			bin := binaryName()
			switch args[0] {
			case "zsh":
				fmt.Fprintf(e.env.Out, zshInit, alias, bin, bin, alias)
			case "bash":
				fmt.Fprintf(e.env.Out, bashInit, alias, bin, bin, alias)
			case "fish":
				fmt.Fprintf(e.env.Out, fishInit, alias, bin, bin, alias)
			default:
				return fmt.Errorf("unknown shell %q (zsh, bash, fish)", args[0])
			}
			return nil
		},
	}
	c.Flags().StringVar(&alias, "alias", "tv", "name of the short command to define")
	return c
}

// binaryName is how this process was invoked, so init works whether the
// binary is on PATH as tvctl or sits somewhere else.
func binaryName() string {
	if p, err := os.Executable(); err == nil {
		if _, lookErr := execLookPath(filepath.Base(p)); lookErr == nil {
			return filepath.Base(p)
		}
		return p
	}
	return "tvctl"
}

const zshInit = `# tvctl shell integration: %[1]s fronts %[2]s
%[1]s() {
  if [ $# -eq 0 ]; then TVCTL_ALIAS=%[1]s command %[2]s; else TVCTL_ALIAS=%[1]s command %[2]s "$@"; fi
}
if (( $+functions[compdef] )); then
  eval "$(command %[3]s completion zsh 2>/dev/null)"
  compdef _%[3]s %[4]s 2>/dev/null
fi
`

const bashInit = `# tvctl shell integration: %[1]s fronts %[2]s
%[1]s() {
  if [ $# -eq 0 ]; then TVCTL_ALIAS=%[1]s command %[2]s; else TVCTL_ALIAS=%[1]s command %[2]s "$@"; fi
}
if type complete >/dev/null 2>&1; then
  eval "$(command %[3]s completion bash 2>/dev/null)"
  complete -o default -F __start_%[3]s %[4]s 2>/dev/null
fi
`

const fishInit = `# tvctl shell integration: %[1]s fronts %[2]s
function %[1]s
  if test (count $argv) -eq 0
    TVCTL_ALIAS=%[1]s command %[2]s
  else
    TVCTL_ALIAS=%[1]s command %[2]s $argv
  end
end
command %[3]s completion fish 2>/dev/null | source
complete -c %[4]s -w %[3]s
`
