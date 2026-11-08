// Package trace prints where the time goes, when asked. Set TVCTL_DEBUG=1
// and every stage worth timing writes one line to stderr with the seconds
// since the process started. stderr is never the spoken line, so an
// assistant host can leave it on and collect the file.
package trace

import (
	"fmt"
	"os"
	"time"
)

var (
	enabled = os.Getenv("TVCTL_DEBUG") != ""
	start   = time.Now()
)

// Logf records a stage. No-op unless TVCTL_DEBUG is set.
func Logf(format string, a ...any) {
	if !enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "[%6.2fs] %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, a...))
}
