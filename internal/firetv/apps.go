package firetv

import "github.com/omAthalye/tvctl/internal/android"

// Fire TV's app list (appsV2) omits Amazon's own apps and sideloaded ones,
// so well-known names are answered from the Android table first.

func (c *Client) AppID(name string) (string, bool) { return android.Package(name) }

func (c *Client) AppName(id string) string {
	if n := android.AppName(id); n != id {
		return n
	}
	return ""
}
