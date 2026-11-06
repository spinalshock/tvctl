//go:build lite

package firetv

import (
	"context"
	"io"

	"github.com/spinalshock/tvctl/internal/device"
)

// InstallCompanion in the lite build says where to do it instead. The
// method still exists so the TV is recognised as one that can take the
// companion app, and errors keep saying "needs the companion app".
func (c *Client) InstallCompanion(context.Context, io.Writer, func(string) (string, error)) (string, error) {
	return "", device.Fail("installing the companion app needs the full tvctl build").Note("run tv companion from a computer")
}
