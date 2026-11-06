package cli

// The TV brands tvctl speaks. Each registers itself with the device
// registry; nothing else in the CLI or TUI names a brand.
import (
	_ "github.com/spinalshock/tvctl/internal/firetv"
	_ "github.com/spinalshock/tvctl/internal/webos"
)
