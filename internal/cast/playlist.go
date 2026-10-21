// Package cast serves local files to the TV and walks through them as a
// playlist. It is shared by `tv play` (blocking) and the TUI's `files`
// flow (event-driven), so the serving and sequencing logic exists once.
//
// Mechanics: one HTTP server on an ephemeral port bound to the interface
// that reaches the TV, serving only the chosen files by index. The TV's
// native player (companion app) is pointed at each URL in turn. Range
// requests are honoured by Go's file server, which is what makes seeking
// work on the TV side.
package cast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/receiver"
)

// ReceiverPackage is the companion app's package id, used to recognise
// our own player in now-playing documents.
const ReceiverPackage = "dev.tvctl.receiver"

// videoExts is what Expand picks out of a directory. ExoPlayer handles
// more, but these are the ones people actually have.
var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".m4v": true,
	".avi": true, ".ts": true, ".mp3": true, ".m4a": true, ".flac": true, ".aac": true, ".ogg": true, ".wav": true,
}

// Expand turns the user's selection into a flat ordered file list:
// directories contribute their media files sorted by name, one level deep.
func Expand(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			out = append(out, abs)
			continue
		}
		ents, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		var inDir []string
		for _, e := range ents {
			if !e.IsDir() && videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
				inDir = append(inDir, filepath.Join(abs, e.Name()))
			}
		}
		sort.Strings(inDir)
		out = append(out, inDir...)
	}
	if len(out) == 0 {
		return nil, errors.New("no media files selected")
	}
	return out, nil
}

// Playlist is a running session: server plus cursor.
type Playlist struct {
	Files []string
	rc    *receiver.Client
	dev   device.Device
	ln    net.Listener
	srv   *http.Server
	mu    sync.Mutex
	idx   int
}

// Start serves files and begins playing the first one.
func Start(ctx context.Context, rc *receiver.Client, dev device.Device, files []string) (*Playlist, error) {
	ip, err := localIPFor(rc.Host)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return nil, err
	}
	p := &Playlist{Files: files, rc: rc, dev: dev, ln: ln, idx: -1}
	mux := http.NewServeMux()
	// Files are addressed by index, so odd characters in names never
	// matter and nothing outside the chosen set is reachable.
	mux.HandleFunc("/f/", func(w http.ResponseWriter, r *http.Request) {
		i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/f/"))
		if err != nil || i < 0 || i >= len(p.Files) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p.Files[i])
	})
	p.srv = &http.Server{Handler: mux}
	go p.srv.Serve(ln)
	if err := p.Play(ctx, 0); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Play points the TV at file i.
func (p *Playlist) Play(ctx context.Context, i int) error {
	if i < 0 || i >= len(p.Files) {
		return errors.New("playlist: index out of range")
	}
	p.mu.Lock()
	p.idx = i
	p.mu.Unlock()
	name := filepath.Base(p.Files[i])
	title := strings.TrimSuffix(name, filepath.Ext(name))
	url := fmt.Sprintf("http://%s/f/%d", p.ln.Addr().String(), i)
	needs, err := p.rc.Play(ctx, url, title)
	if err != nil {
		return err
	}
	if needs {
		// Basic mode: the receiver can't start its player from the
		// background; the TV's own API can bring the app forward.
		return p.dev.Launch(ctx, ReceiverPackage)
	}
	return nil
}

// Next advances; false when the playlist is exhausted.
func (p *Playlist) Next(ctx context.Context) (bool, error) {
	p.mu.Lock()
	i := p.idx + 1
	p.mu.Unlock()
	if i >= len(p.Files) {
		return false, nil
	}
	return true, p.Play(ctx, i)
}

// Index returns the current position (0-based) and the total.
func (p *Playlist) Index() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idx, len(p.Files)
}

// Current is the file now pointed at.
func (p *Playlist) Current() string {
	i, _ := p.Index()
	if i < 0 {
		return ""
	}
	return filepath.Base(p.Files[i])
}

// Addr is where the files are served from, for status lines.
func (p *Playlist) Addr() string { return p.ln.Addr().String() }

// Close stops serving. The TV keeps whatever it has buffered.
func (p *Playlist) Close() {
	if p.srv != nil {
		_ = p.srv.Close()
	}
}

// Finished reports whether a now-playing document means the current item
// is over: our player reached the end, or the user left the player.
func Finished(n *receiver.Now) bool {
	if n == nil {
		return false
	}
	if n.App == ReceiverPackage {
		return n.State == "stopped"
	}
	// Not our player at all: either nothing is playing or something else
	// took over. Both mean we're done with this item.
	return n.Foreground == nil || *n.Foreground != ReceiverPackage
}

// localIPFor finds the local address the OS would use to reach host by
// "connecting" a UDP socket; no packets are sent.
func localIPFor(host string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(host, "9"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
