// Package tts turns a phrase into raw PCM audio in the one format Fire TV's
// voice channel accepts: 16 kHz, mono, signed 16-bit little-endian. Each OS
// has a built-in or commonly installed synthesizer; we shell out to it and
// normalise the result, so callers never care which one ran.
package tts

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	SampleRate = 16000
	Channels   = 1
	BitDepth   = 16
)

// Synthesize returns PCM bytes for phrase. voice is an optional engine
// voice name ("" = engine default).
func Synthesize(ctx context.Context, phrase, voice string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "tvctl-tts-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "out.wav")

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// `say` can write exactly our format directly.
		args := []string{"-o", wav, "--data-format=LEI16@16000"}
		if voice != "" {
			args = append(args, "-v", voice)
		}
		cmd = exec.CommandContext(ctx, "say", append(args, phrase)...)
	case "windows":
		// System.Speech ships with Windows. SpeechAudioFormatInfo lets us
		// request 16k/16-bit/mono so no resampling is needed.
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Speech;
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer;
%s
$f = New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(16000, [System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen, [System.Speech.AudioFormat.AudioChannel]::Mono);
$s.SetOutputToWaveFile(%q, $f);
$s.Speak(%q);
$s.Dispose()`, psVoice(voice), wav, phrase)
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		// Linux: prefer piper (natural voices), else espeak-ng. Both are
		// resampled to 16k by the WAV reader below if they emit 22.05k.
		switch {
		case have("piper"):
			cmd = exec.CommandContext(ctx, "sh", "-c",
				fmt.Sprintf("printf %%s %q | piper --output_file %q", phrase, wav))
		case have("espeak-ng"):
			cmd = exec.CommandContext(ctx, "espeak-ng", "-w", wav, phrase)
		default:
			return nil, errors.New("no speech engine found: install piper or espeak-ng")
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tts: %w: %s", err, out)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		return nil, err
	}
	return pcmFromWAV(b)
}

func psVoice(v string) string {
	if v == "" {
		return ""
	}
	return fmt.Sprintf("$s.SelectVoice(%q);", v)
}

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// pcmFromWAV walks the RIFF chunks, checks the format, and returns the data
// chunk. It converts stereo to mono and 8-bit to 16-bit, and resamples by
// linear interpolation when the rate isn't 16 kHz, which is good enough for
// speech recognition (Alexa only needs intelligible audio, not fidelity).
func pcmFromWAV(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("tts: engine did not produce a WAV file")
	}
	var (
		rate, ch, bits int
		data           []byte
	)
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		sz := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := b[off+8:]
		if sz > len(body) {
			sz = len(body)
		}
		body = body[:sz]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("tts: short fmt chunk")
			}
			ch = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			data = body
		}
		off += 8 + sz + sz%2 // chunks are word-aligned
	}
	if data == nil || rate == 0 {
		return nil, errors.New("tts: WAV missing fmt or data chunk")
	}
	// Decode to int16 mono samples.
	var samples []int16
	switch bits {
	case 16:
		n := len(data) / 2 / ch
		samples = make([]int16, n)
		for i := 0; i < n; i++ {
			var sum int
			for c := 0; c < ch; c++ {
				sum += int(int16(binary.LittleEndian.Uint16(data[(i*ch+c)*2:])))
			}
			samples[i] = int16(sum / ch)
		}
	case 8:
		n := len(data) / ch
		samples = make([]int16, n)
		for i := 0; i < n; i++ {
			var sum int
			for c := 0; c < ch; c++ {
				sum += (int(data[i*ch+c]) - 128) << 8
			}
			samples[i] = int16(sum / ch)
		}
	default:
		return nil, fmt.Errorf("tts: unsupported bit depth %d", bits)
	}
	if rate != SampleRate {
		samples = resample(samples, rate, SampleRate)
	}
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out, nil
}

func resample(in []int16, from, to int) []int16 {
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		if j+1 >= len(in) {
			out[i] = in[len(in)-1]
			continue
		}
		frac := pos - float64(j)
		out[i] = int16(float64(in[j])*(1-frac) + float64(in[j+1])*frac)
	}
	return out
}
