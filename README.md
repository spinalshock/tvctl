# tvctl

Control a TV from the terminal, an interactive prompt, or an LLM agent.
One static Go binary, no daemon: each command opens a connection to the
TV, does one thing, prints one line, and exits.

```
$ tv pair
Allow it on the TV.
Connected to [LG] webOS TV UR8040PSB.

$ tv open netflix
Opening Netflix.

$ tv now
Netflix is paused.

$ tv input reliance
Switched to Reliance Set-Top Box.
```

That one line is written to be read aloud. Point a voice assistant or any
LLM tool-calling loop at `tvctl` and it has a TV remote: see
[docs/driving-tvctl.md](docs/driving-tvctl.md).

## TVs it speaks to

| TV | Protocol | Pairing | Status |
|---|---|---|---|
| LG webOS (2016 and later) | the TV's own WebSocket API | Allow prompt on screen | verified on a 43UR8040 |
| Amazon Fire TV (Stick, Cube, built-in) | the API the official phone app uses | PIN on screen | verified |

Both work with nothing installed on the TV. Brands are drivers behind one
small interface, so adding another is a new package, not a rewrite
(see [Adding a brand](#adding-a-brand)).

### The optional companion app

Android-based TVs (Fire TV today) can take a small companion app that lives
in [`receiver/`](receiver/) and adds what the stock API can't do:

- rich now-playing with title, position and transport control (`now`,
  `pause`, `stop`, `seek`, `events`)
- text and panels pinned over whatever is on screen (`overlay`, `dash`,
  `watch`, longer `toast`)
- screenshots (`shot`)
- playing local files and playlists on the TV (`play ~/videos/*.mp4`)
- a home dashboard of your own tiles in place of the stock launcher (`home`)

Install it with `tv companion` from a machine on the same network as the
TV with ADB debugging turned on. The command pushes the app, pairs it,
and saves its token in your config. tvctl implements the ADB protocol
itself, so no Android SDK is needed on the machine that runs it.

The copy embedded in the binary is a debug-signed build of the source in
`receiver/`. Build your own with `make apk` (JDK 17 and the Android SDK).

## Install

```sh
go install github.com/spinalshock/tvctl/cmd/tvctl@latest
tvctl init zsh >> ~/.zshrc        # or bash/fish: defines the short `tv` command
```

Or clone and `make`, which builds and installs to `$(go env GOPATH)/bin`.
Go 1.26 or later.

## Quick start

```sh
tv devices --scan            # TVs on this network
tv pair                      # one found: pairs with it; or tv pair <ip> --brand webos|firetv
tv pin 1234                  # Fire TV only: the PIN the TV is showing, as a second step

tv key home                  # keys: home back select up down left right play ff rw mute sleep wake stop
tv vol up 3
tv input                     # lists inputs with the owner's labels; tv input hdmi2
tv apps
tv open prime                # alias, name prefix, or the TV's own app id
tv youtube lofi beats        # top YouTube hit
tv play https://youtu.be/... # or words, or files (companion app)
tv now
tv sleep && tv wake          # wake needs Wake-on-LAN enabled on the TV
tv toast "dinner's ready"
```

`tv --help` lists every verb by group. `--json` on any verb returns the
full document instead of the sentence. `-d <name|ip|id>` picks a TV;
without it, the default TV is used, or the paired TV that is on the
network you are currently on.

The config lives at `~/.config/tvctl/config.json` and holds the pairing
tokens. Nothing leaves your network: no accounts, no telemetry, no cloud.

### Interactive prompt

`tv` with no arguments opens a prompt with completion, a now-playing bar,
a keys mode for driving the TV with the arrow keys, and live sync with
the TV's on-screen keyboard when the companion app is present. Every word
you can type there is also a CLI verb.

## For assistants and agents

`tv tools` describes every verb as a tool, with choices, deadlines and the
paired TVs, generated from the command tree. `tv tools --voice` keeps the
spoken subset; `--schema` emits function declarations.

The contract an agent gets: plain stdout is the sentence to say (empty for
routine acks), errors are one sentence with exit code 1, every verb has a
deadline, and verbs that change the TV wake it first. A TV that can't do
something says so by capability, not brand: `webOS TV can't do
screenshots.`, `Fire TV needs the companion app for overlays.`

For an always-on host such as a phone in Termux:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o tvctl ./cmd/tvctl
```

Static, 20 MB, and it can scan, pair and install the companion app from
the phone. `-tags lite` builds a 9 MB variant without the prompt, the ADB
verbs and the embedded app, for hosts that should stay small.

## Adding a brand

A brand is a package under `internal/` that registers a `device.Driver`
(brand name, discovery, open) and implements whichever optional
capabilities its protocol supports: pairing, wake, inputs, apps by name,
toasts, URL opening, now-playing. The CLI and the prompt never name a
brand; they ask the device what it can do. Start from
[`internal/webos`](internal/webos) for a WebSocket API or
[`internal/firetv`](internal/firetv) for HTTP, add a blank import in
`internal/cli/drivers.go`, and copy a `keys_test.go`.

## Notes

- Wake-on-LAN needs the TV's MAC, which tvctl learns on pairing or after
  the first successful command, and the TV's own "turn on via network"
  setting (LG calls it Turn on via Wi-Fi).
- YouTube videos play through YouTube's own casting flow (DIAL plus the
  lounge API) on every brand, which needs no account; `tv youtube <words>`
  plays the top search hit. On Fire TV the `youtube` alias opens SmartTube,
  an ad-free client, by choice; change the table in `internal/android` if
  you use another.
- `shot` shows black over DRM video; menus and most apps are fine.
- The companion app runs an accessibility service to read the on-screen
  keyboard and now-playing; it serves HTTP on the LAN with the token tvctl
  pairs it with, and never talks to anything else.

## Contributing

Issues and pull requests are welcome. Keep brand facts in the brand's
package, keep every verb working without the companion app unless it is a
companion feature by nature, and run `go test ./...` for both build
variants (`-tags lite` too) before opening a PR.

## License

[MIT](LICENSE).
