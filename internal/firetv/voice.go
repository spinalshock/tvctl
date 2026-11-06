package firetv

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/tts"
)

// Say delivers a phrase to Alexa as if spoken into the remote's microphone.
//
// The voice channel is three steps the phone app performs when you hold its
// mic button:
//
//  1. POST /v1/FireTV/voiceCommand?action=start     "mic pressed"
//  2. stream raw PCM frames over wss://tv:9090/      the audio
//  3. close the socket, POST ...?action=stop        "mic released"
//
// Closing the socket is what marks end-of-utterance; Alexa then processes
// what it heard and acts on it with its own system privileges. That's why
// this can switch HDMI inputs when nothing else on the local API can.
//
// Frames are 100 ms each and sent at real-time pace. Bursting the whole
// clip at once makes recognition unreliable (the server appears to treat
// arrival timing as part of the signal), so the sleeps are load-bearing.
func (c *Client) Say(ctx context.Context, phrase string) error {
	pcm, err := tts.Synthesize(ctx, phrase, "")
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, "/v1/FireTV/voiceCommand?action=start", nil, nil); err != nil {
		return err
	}
	// Always send stop, even on failure, or the TV's mic state sticks on.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.do(stopCtx, http.MethodPost, "/v1/FireTV/voiceCommand?action=stop", nil, nil)
	}()

	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		// Same HTTP/1.1-only rule as the REST side.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}
	hdr := http.Header{}
	hdr.Set("x-api-key", apiKey)
	hdr.Set("x-client-token", c.Token)
	hdr.Set("User-Agent", userAgent)
	wsURL := "wss://" + net.JoinHostPort(c.Host, "9090") + "/"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: hc, HTTPHeader: hdr})
	if err != nil {
		return device.Fail("the TV's voice channel didn't open").Because(err)
	}

	const frame = tts.SampleRate * tts.Channels * (tts.BitDepth / 8) / 10 // 100 ms
	// Pad with real silence on both ends. A person holding the mic button
	// pauses before and after speaking; the recogniser expects that gap and
	// drops the first syllable when audio begins the instant the socket
	// opens. The pause must be audio frames, not a sleep: silence the
	// server never receives is not silence it can hear.
	pcm = append(append(make([]byte, 5*frame, 5*frame+len(pcm)+8*frame), pcm...), make([]byte, 8*frame)...)
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := ws.Write(ctx, websocket.MessageBinary, pcm[off:end]); err != nil {
			ws.Close(websocket.StatusInternalError, "write failed")
			return device.Fail("the voice stream to the TV broke off").Because(err)
		}
		select {
		case <-ctx.Done():
			ws.Close(websocket.StatusNormalClosure, "")
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	// The TV never answers the WebSocket close handshake; it treats the TCP
	// close itself as end-of-utterance. CloseNow skips the handshake wait.
	return ws.CloseNow()
}
