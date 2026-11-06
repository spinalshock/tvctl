// Command tvctl: with arguments it runs one CLI command; with none it opens the
// TUI. Both share the command tree in internal/cli.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spinalshock/tvctl/internal/cli"
)

func main() {
	if len(os.Args) == 1 {
		if err := openTUI(); err != nil {
			fmt.Fprintln(os.Stderr, "tv:", err)
			os.Exit(1)
		}
		return
	}
	env := &cli.Env{Out: os.Stdout, Ask: askStdin, Interactive: isTerminal(os.Stdin)}
	if err := cli.Run(os.Args[1:], env); err != nil {
		// `tv watch cmd` ends with cmd's status so scripts can chain on it.
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}

// isTerminal reports whether f is a terminal, i.e. a person can answer a
// question. A pipe or /dev/null (a script, a voice assistant) can't, and
// commands that would ask then say what to run next instead.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// askStdin reads one line from the terminal, for the pairing PIN and
// multiple-choice prompts. The prompt goes to stderr so --json stdout stays
// clean.
func askStdin(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt+": ")
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", errors.New("no input")
	}
	return strings.TrimSpace(sc.Text()), nil
}
