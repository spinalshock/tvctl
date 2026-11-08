// Package youtube implements the sender side of YouTube's "lounge" casting
// protocol, which is how the YouTube phone app plays a video on a TV that
// only speaks DIAL (Fire TV, many smart TVs). It is brand-independent: any
// driver that can POST to the TV's DIAL YouTube endpoint can use it.
//
// The flow, as observed against a Fire TV:
//
//  1. DIAL-launch the YouTube app with a pairingCode we invent. The app
//     registers that code with YouTube's servers against its screen id.
//  2. POST pairing/get_screen with the code; YouTube returns the screen's
//     lounge token, the credential for driving that screen.
//  3. POST bc/bind once to open a session (returns SID and gsessionid).
//  4. POST bc/bind again carrying a setPlaylist command with the video id.
//
// Steps 2-4 go to youtube.com, not the TV. No Google account is involved;
// the lounge token only proves we were on the TV's LAN when it registered.
package youtube

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
)

const (
	loungeBase = "https://www.youtube.com/api/lounge"
	dialOrigin = "https://www.youtube.com"
)

// Launcher is what the caller provides: a way to DIAL-launch the YouTube
// app with a body and headers. Fire TV's driver passes its dial helper.
type Launcher func(ctx context.Context, app, body string, hdr map[string]string) error

// Play opens videoID on the TV at startSeconds. It returns once the play
// command is accepted by YouTube; the TV typically starts within a second.
func Play(ctx context.Context, launch Launcher, videoID string, startSeconds int) error {
	code := uuid()
	if err := launch(ctx, "YouTube", "pairingCode="+code+"&theme=cl", map[string]string{"Origin": dialOrigin}); err != nil {
		return device.Fail("couldn't start YouTube on the TV").Because(err)
	}
	// The TV registers the code asynchronously, and slowly when the app is
	// already busy playing; poll for up to ~15 s.
	var tok string
	var err error
	for i := 0; i < 20; i++ {
		tok, err = screenToken(ctx, code)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}
	if err != nil {
		return device.Fail("YouTube didn't recognise the TV").Note("the TV's YouTube app never registered with YouTube").Because(err)
	}
	// The lounge endpoints occasionally answer a fresh session with an
	// HTML error page; one retry after a short pause clears it.
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		s, err := bind(ctx, tok)
		if err == nil {
			err = s.setPlaylist(ctx, videoID, startSeconds)
		}
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return last
}

// screenToken exchanges the pairing code for the screen's lounge token.
func screenToken(ctx context.Context, code string) (string, error) {
	var res struct {
		Screen struct {
			LoungeToken string `json:"loungeToken"`
		} `json:"screen"`
	}
	if err := postJSON(ctx, loungeBase+"/pairing/get_screen", url.Values{"pairing_code": {code}}, &res); err != nil {
		return "", err
	}
	if res.Screen.LoungeToken == "" {
		return "", device.Fail("YouTube gave an unexpected answer").Note("no lounge token in response")
	}
	return res.Screen.LoungeToken, nil
}

// session is one bound lounge connection. RID is a per-request counter
// YouTube expects to increase; AID is the last acknowledged server message
// id, which is 0 for us because we never read the stream.
type session struct {
	token, id, sid, gsession string
	rid                      int
}

var (
	reSID = regexp.MustCompile(`\["c","([^"]+)"`)
	reGS  = regexp.MustCompile(`\["S","([^"]+)"\]`)
)

func bind(ctx context.Context, token string) (*session, error) {
	s := &session{token: token, id: uuid(), rid: 1000 + int(time.Now().UnixNano()%9000)}
	q := s.query()
	body, err := postRaw(ctx, loungeBase+"/bc/bind?"+q.Encode(), url.Values{"count": {"0"}})
	if err != nil {
		return nil, device.Fail("couldn't reach YouTube").Because(err)
	}
	// The response is a chunked stream of JSON arrays prefixed by byte
	// lengths. We only need two values from the first chunk.
	m := reSID.FindStringSubmatch(body)
	g := reGS.FindStringSubmatch(body)
	if m == nil || g == nil {
		return nil, device.Fail("YouTube gave an unexpected answer").Notef("bind: %.120q", body)
	}
	s.sid, s.gsession = m[1], g[1]
	return s, nil
}

// query builds the common bind parameters. device=REMOTE_CONTROL and
// app=youtube-desktop are what the web sender uses; the TV accepts them.
func (s *session) query() url.Values {
	s.rid++
	q := url.Values{
		"device":        {"REMOTE_CONTROL"},
		"id":            {s.id},
		"name":          {"tvctl"},
		"app":           {"youtube-desktop"},
		"mdx-version":   {"3"},
		"loungeIdToken": {s.token},
		"VER":           {"8"},
		"v":             {"2"},
		"RID":           {fmt.Sprint(s.rid)},
		"CVER":          {"1"},
		"zx":            {zx()},
		"t":             {"1"},
	}
	if s.sid != "" {
		q.Set("SID", s.sid)
		q.Set("gsessionid", s.gsession)
		q.Set("AID", "0")
	}
	return q
}

// setPlaylist replaces the TV's queue with one video and starts it.
func (s *session) setPlaylist(ctx context.Context, videoID string, start int) error {
	form := url.Values{
		"count":             {"1"},
		"ofs":               {"0"},
		"req0__sc":          {"setPlaylist"},
		"req0_videoId":      {videoID},
		"req0_currentTime":  {fmt.Sprint(start)},
		"req0_listId":       {""},
		"req0_currentIndex": {"-1"},
		"req0_prioritizeMobileSenderPlaybackStateOnConnection": {"true"},
	}
	_, err := postRaw(ctx, loungeBase+"/bc/bind?"+s.query().Encode(), form)
	if err != nil {
		return device.Fail("YouTube didn't take the video").Because(err)
	}
	return nil
}

// ---- small helpers --------------------------------------------------------

var httpc = &http.Client{Timeout: 15 * time.Second}

func postRaw(ctx context.Context, u string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		if strings.Contains(strings.ToLower(string(b[:min(len(b), 200)])), "<html") {
			return "", device.Fail("YouTube refused").Notef("HTTP %d", resp.StatusCode)
		}
		return "", device.Fail("YouTube refused").Notef("HTTP %d: %.200s", resp.StatusCode, b)
	}
	return string(b), nil
}

func postJSON(ctx context.Context, u string, form url.Values, out any) error {
	b, err := postRaw(ctx, u, form)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), out)
}

// uuid is a random v4 UUID string; the pairing code and sender id are only
// required to be unique, not meaningful.
func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// zx is the cache-buster YouTube's client appends; any 12 lowercase
// alphanumerics work.
func zx() string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [12]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b[:])
}
