//go:build !lite

package firetv

import (
	"context"
	"io"

	"github.com/spinalshock/tvctl/internal/setup"
)

// InstallCompanion is the one ADB step: install or update the app, grant
// its permissions, pair with it. Both the stock and the composed client
// offer it, so re-running it updates the app in place. The lite build
// (install_lite.go) leaves the installer and its embedded APK out.
func (c *Client) InstallCompanion(ctx context.Context, out io.Writer, ask func(string) (string, error)) (string, error) {
	return setup.Run(ctx, c.Host, c.companionToken, setup.Options{Out: out, Ask: ask, Launch: c.Launch})
}
