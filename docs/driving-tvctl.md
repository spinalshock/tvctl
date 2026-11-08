# Driving tvctl from an agent

How any LLM agent or local model drives a TV through tvctl: a Claude session,
a FunctionGemma loop on a phone, or a shell script. The phone setup is one
host among several; this is the contract every host gets.

## The contract

- **One binary, one process per command.** `tvctl <verb> [args]` (or the
  `tv` alias from `tvctl init zsh`). No daemon, nothing to keep alive.
- **Plain stdout is the line to speak, verbatim.** Empty stdout means the
  command was a routine ack (key press, volume, toast) and nothing should be
  said. One short sentence means an outcome: `Opening Netflix.`,
  `Switched to Reliance Set-Top Box.`, `TV off.`, `Netflix is paused.`
- **Errors are one sentence on stdout too, with exit code 1.** They go to
  stdout on purpose: the host speaks whatever came out, success or failure,
  and checks the exit code to know which. No prefix, no "run tv pair" hints: `webOS TV UR8040PSB is off or unreachable.`,
  `No app called foo on this TV.` Speak it as-is.
- **A verb the TV can't do says so, by capability, not by brand.** Every
  verb asks the TV's driver for the capability it needs. An LG asked for a
  screenshot answers `webOS TV UR8040PSB can't do screenshots.` A Fire TV
  without the companion app answers `Fire TV needs the companion app for
  screenshots.` The companion app is an Android app and only Android-based
  TVs can take it; on them it adds overlays, screenshots, the home
  dashboard, local file playback, rich now-playing and transport control.
- **`--json` gives the full document** (and the full error text under
  `"error"`) for hosts that want structure instead of speech. Every error
  tvctl produces is written as a sentence plus notes: the sentence is what
  plain mode prints, the notes (a cause, a hint such as "run tv pair") are
  appended in brackets only in `--json`. Example: plain `Not paired with
  the LG.`, JSON `"not paired with the LG (run tv pair)"`.
- **Every verb has a deadline.** A TV that does not answer produces
  `<TV name> didn't answer in time.` within the verb's budget (5s for a key,
  8s volume, 15s default, 25s for voice-assistant verbs). Nothing hangs.
  Streaming verbs (`events`, `watch`, `play` with files) are the exception
  and are marked `blocking` in the manifest.
- **Verbs that change the TV wake it first.** `key`, `vol`, `input`, `open`,
  `play`, `youtube`, `search`, `say`, `text` probe the TV and send
  Wake-on-LAN when it is in standby, then do what was asked. Measured on
  the LG UR8040: `open netflix` from standby, 2.5s. The manifest marks
  these `wakes: true`.

## The manifest

```sh
tvctl tools --voice            # the 15 spoken verbs, with choices, deadlines, paired TVs
tvctl tools --schema --voice   # same, as OpenAI-style function declarations
tvctl tools                    # every verb, including companion-app ones
```

Regenerate at startup rather than hard-coding: the list is produced from the
command tree, so it never drifts from the binary you have.

Mapping a function call back to argv is mechanical:

| Function call | Command |
|---|---|
| `tv_key {"key": "home"}` | `tvctl key home` |
| `tv_vol {"direction": "up", "n": "3"}` | `tvctl vol up 3` |
| `tv_input {"input": "hdmi2"}` | `tvctl input hdmi2` |
| `tv_open {"app": "netflix"}` | `tvctl open netflix` |
| `tv_play {"words": "tambdi chambdi"}` | `tvctl play tambdi chambdi` |
| `tv_youtube {"words": "lofi beats"}` | `tvctl youtube lofi beats` |
| `tv_now {}` | `tvctl now` |

Parameters are positional in declaration order. A parameter described as
"the words, space separated" is split on spaces into several argv entries.
Enums come straight from the verb's choices.

Run with stdin not attached to a terminal (`< /dev/null` or a pipe). tvctl
notices and takes its non-interactive paths, which matters for pairing.

## Which TV

`--device <name|ip|id>` picks a TV. Without it, the config's `default`
entry is used, unless that TV is not on any network this host is on, in
which case the paired TV that is on the local network is used (lowest id
if several). So the same config works in both houses with no flag:
whichever TV is on the network you are on answers. Saved TVs are keyed by LAN
IP and MAC; never the Tailscale 100.x address.

## Setup on a new host

```sh
tvctl devices --scan                 # finds TVs on this network (LG answers in ~4s)
tvctl pair                           # one TV found: pairs with it
tvctl pair 192.168.29.76             # or by address
```

- **LG (webOS):** the TV shows an Allow prompt. `pair` prints
  `Allow it on the TV.` and waits up to 90s for the remote's OK, then
  `Connected to <name>.` It records the TV's MAC for Wake-on-LAN at the same
  time. "Turn on via Wi-Fi" must be enabled on the TV (it is on the UR8040).
- **Fire TV:** `pair` puts a PIN on screen and prints `Tell me the PIN on the
  TV.` and exits. `tvctl pin 1234` finishes it as a separate command. No
  process has to stay alive in between, and ADB is never needed for this.
- A TV paired before MAC capture existed gets its MAC saved automatically the
  first time any command succeeds against it.

The config file is `~/.config/tvctl/config.json` on every platform
(XDG, also inside Termux). It holds the pairing tokens, so keep it private.

## Verified on hardware, 5 Nov 2026, LG 43UR8040PSB (webOS)

From scratch: scan, pair with the Allow prompt (8s), then `status`, `now`
(`Live TV is open.`, `Netflix is paused.`), `input list`, `input hdmi1`,
`input reliance` (matches the owner's label), `vol up/down`, `key back/home`,
`toast`, `apps`, `open netflix` / `youtube` / `prime` (shortcut names resolve
to LG's own app ids), `sleep`, and auto-wake from standby via `key home`
(2.4s) and `open netflix` (2.5s). `tv wake` from standby: 1.8s. On 8 Nov
2026: `youtube <words>` and `play <youtube link>` play through the cast
path (the app's own deep link only opened it; `now` then says "YouTube is
playing."), and the screen saver no longer counts as standby. `pause`,
`play` and `stop` verified against that video.

Not yet re-verified on the Fire TV after today's changes: two-step PIN
pairing, `play`, `seek next`, auto-wake. Those paths are unit-tested
against a fake driver only.

## Termux notes for the phone host

- Build on the Mac: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o tvctl ./cmd/tvctl`.
  Static, no cgo, runs in Termux as-is (20 MB). This full build is the one
  for the phone: it is a self-contained universal remote that can scan a
  new network, pair with any supported TV, and install the companion app
  on a Fire TV over the network, with no laptop involved (the ADB protocol
  is implemented in Go). Add `-tags lite` only for a host that should stay
  small (9 MB): it drops the interactive prompt, the ADB verbs and the
  installer with its embedded APK.
- The webOS driver asks the TV for its MAC, so the ARP fallback
  (`/proc/net/arp`, which Android restricts) is not needed for the LG.
- Android also refuses the interface list to apps (netlink permission
  denied), which is how "which TV is local" is normally decided. tvctl then
  asks the kernel for source addresses instead: the default route's source
  is the Wi-Fi address, and a TV in its /24 is local even when a Tailscale
  subnet route for that very LAN pulls the traffic into the tunnel; failing
  that, a TV whose own source is a Tailscale address is not local. Behind
  an exit node nothing can be told apart and the default TV wins; pass -d.
  Fixed 7 Nov 2026 after the phone kept picking the other home's TV.
- Android also has no /etc/resolv.conf, which leaves Go's resolver with
  nowhere to send lookups; tvctl reads Termux's resolv.conf (or falls back
  to public resolvers) so YouTube search and casting resolve. LAN traffic
  by IP never depended on this. Fixed 8 Nov 2026.
- Keep the phone awake and online: `termux-wake-lock`, battery optimisation
  off for Termux, Termux:Boot to start the assistant loop, and
  `termux-services` (runit) if the loop should be a supervised service.
  tvctl itself needs none of this; it is only ever a short-lived process.
- Low resource use is by design: each command is one process that opens one
  WebSocket, does its work and exits, typically in 0.2s to 0.5s on the LAN.

## What is not in scope yet

Device aliases and presence ("which TV am I near"), a resident `tv serve`
with warm connections, companion-app self-update, and further brands (Roku,
Android TV, Samsung).
