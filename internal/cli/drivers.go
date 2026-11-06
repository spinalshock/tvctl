package cli

// The TV brands tvctl speaks. Each registers itself with the device
// registry; nothing else in the CLI or TUI names a brand.
import (
	_ "github.com/omAthalye/tvctl/internal/firetv"
	_ "github.com/omAthalye/tvctl/internal/webos"
)
