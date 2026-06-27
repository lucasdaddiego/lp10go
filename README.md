# lp10

> One command, one screen — a terminal player and equalizer for the **Arylic LP10**
> network audio streamer, driven over a single SSH connection.

[![CI](https://github.com/lucasdaddiego/lp10go/actions/workflows/ci.yml/badge.svg)](https://github.com/lucasdaddiego/lp10go/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/lucasdaddiego/lp10go)](https://goreportcard.com/report/github.com/lucasdaddiego/lp10go)
[![Go Reference](https://pkg.go.dev/badge/github.com/lucasdaddiego/lp10go.svg)](https://pkg.go.dev/github.com/lucasdaddiego/lp10go)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![go](https://img.shields.io/badge/go-1.24%2B-00ADD8)
![platform](https://img.shields.io/badge/platform-macOS-lightgrey)

```
$ lp10
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃                                                                              ┃
┃  ♪ LP10 · Living  ● 15:42           Spotify · audio/ogg · 44.1 kHz    Vol    ┃
┃                                                                              ┃
┃  ╭────────────────────────╮                                            ░     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│  De Música Ligera                          █     ┃
┃  │████████████████████████│  Soda Stereo · Canción Animal              █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│  ▶ Playing 00:30 ━●──────────── -03:01     █     ┃
┃  │████████████████████████│       ◀◀         pause         ▶▶          █     ┃
┃  │████████████████████████│                                            █     ┃
┃  │████████████████████████│                                            █     ┃
┃  ╰────────────────────────╯                                           92%    ┃
┃  ─────────────────────────────── equalizer ────────────────────────────────  ┃
┃  EQ      ● on                                                                ┃
┃  Treble  ────────────────────────────────────────●─────────────────────  +3  ┃
┃  Mid     ────────────────────────●─────────────────────────────────────  -2  ┃
┃  Bass    ────────────────────────────────────────●─────────────────────  +3  ┃
┃  Sub     ● on                                                                ┃
┃  Lvl     ─────────────────────────────────────●────────────────────────  60  ┃
┃  Max Vol ─────────────────────────────────────────────────────────────● 100  ┃
┃          space play · ↑↓ vol · m mute · y copy · e/tab EQ · ? diag · q quit  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

`lp10` turns the Arylic LP10 (a LibreWireless / LUCI network streamer) into a
live terminal dashboard — now-playing, transport, a graphic equalizer, and a
diagnostics overlay — from a single statically-linked Go binary. No companion
app, no browser, no background daemon: run `lp10`, get one screen.

## Features

- **Live now-playing** — title, artist · album, source / quality, a seek bar,
  and segmented transport buttons. The art panel shows the **real album cover**
  — true pixels via the Kitty graphics protocol on Ghostty / kitty, a 24-bit
  half-block raster on any other truecolor terminal, falling back to an animated
  plasma motif for radio / idle / lesser terminals. The title and artist are
  clickable (OSC 8) and link to Spotify.
- **Graphic equalizer** — the EQ switch and treble / mid / bass tone, a deep-bass
  switch and level, and the output cap (Max Volume) — driven over the device's
  own control channel. Paints instantly from a cached snapshot on launch.
- **Diagnostics overlay** (`?`) — a two-column card grid on a wide terminal (a
  stacked read-out when narrow): device & firmware; the active network link (Wi-Fi
  or ethernet, with live throughput, Wi-Fi **SNR**, and round-trip latency — jitter,
  peak, and a rolling sparkline — to your laptop, the gateway, and the internet);
  the **audio chain** (ALSA playback state, the **buffer fill**, and the DAC's
  *actual* rate/format/channels vs the source — catching resampling); and resource
  gauges (cpu + clock · memory · temp · process contention · data). Gathered on the
  device **only while the overlay is open**; any metric the hardware can't provide
  degrades to "—".
- **Finds the device itself** — mDNS auto-discovery at startup locates the LP10 on
  the LAN by its `am=LP10` advertisement, so a changed DHCP lease never needs a
  config edit. Pure mDNS (no dependency, no bound port); falls back to the
  configured host.
- **Mouse, too** — click the transport buttons, click or drag the volume rail,
  scroll to nudge the volume (or the EQ band under the cursor), and click an EQ
  band to grab and set it. Keyboard-first; the mouse is a convenience on top, and
  can be turned off (`mouse = false`) to keep native terminal text selection.
- **Adapts to the terminal** — the full dashboard, a compact frame, or a
  one-line mini view, by size.
- **Light on both ends** — one ssh connection, a single static binary, and an
  on-device shell loop trimmed to the minimum of work (see [How it works](#how-it-works)).

## Install

Requires **macOS**, a recent **Go** toolchain (1.24+), and **OpenSSH** (already
on macOS). Nothing else at runtime.

```sh
# 1. Store the device's root password in the macOS Keychain (once). -w with no
#    value prompts interactively, so the password never lands in shell history
#    or `ps` output.
security add-generic-password -U -a root -s lp10 -w

# 2. Build a stripped release binary into ~/.bin (make sure it's on your PATH).
make install

# 3. Run — no arguments, just one screen.
lp10
```

## Keys

The screen is a two-pane dashboard — the **player** (now-playing + transport)
and the **equalizer**. `tab` moves focus between them; the focused pane drives
the arrow keys.

| Key | Action |
|-----|--------|
| `space` | play / pause |
| `n` / `p` | next / previous track |
| `+` / `-` | volume ± step |
| `↑` / `↓` | player: volume ± step · equalizer: pick a band |
| `←` / `→` | player: move button focus · equalizer: adjust the focused band |
| `enter` | player: press the focused button · equalizer: toggle an on/off band |
| `tab` | switch pane (player ↔ equalizer) |
| `e` | jump focus to the equalizer |
| `m` | mute (volume 0 ↔ restored level, persisted) |
| `t` | right-hand time: remaining ↔ total |
| `y` | copy now-playing (`Title — Artist · Album`) to the clipboard |
| `?` | diagnostics overlay (see below) |
| `q` | quit |

The view adapts to the terminal size: the full **dashboard** (now-playing with
album-motif art, a vertical volume slider, and the graphic equalizer) at ≥ 25
rows / 70 cols, a **compact** frame (no art, inline volume, one-line EQ summary)
below that, and a one-line **mini** view below 9 rows / 58 cols.

### Mouse

On by default (disable with `mouse = false`). The gestures track what you see:

- **Transport** — click prev / play-pause / next (and, in the compact frame, the
  mute button).
- **Volume** — click or drag the vertical rail to set the level; the wheel nudges
  it by `vol_step` from anywhere not over a control.
- **Equalizer** (full dashboard) — click a slider row to focus it; the wheel over a
  row nudges that band; click an on/off band to flip it, or click/drag along a tone
  band's track to set it by position.

Capturing the mouse means the terminal's own click-to-select is suppressed while
lp10 runs; set `mouse = false` if you'd rather keep native selection. There's no
seek/scrub — the device exposes no seek command.

## Equalizer

The equalizer pane (focus it with `e` or `tab`) drives the device's tone and
output as a stack of horizontal sliders — the **EQ** switch, the **Treble / Mid /
Bass** tone, the deep-bass **Sub** switch and its **Lvl**, and **Max Vol**, the
output cap, kept last as it's rarely touched. `↑` / `↓` pick a row; `←` / `→`
adjust it; `enter` flips an on/off band.

These ride a separate plain-text control connection to the device on TCP
**2018** (the same channel the vendor app uses), independent of the SSH player
stream — so a dead tunnel only greys out the equalizer, it never disturbs
playback, and the last-known values are restored instantly from cache on launch.

> **Heads-up:** a low **Max Volume** is what makes the IR remote and Spotify
> seem unable to turn the volume up (they hit the cap). Set it to 100 for the
> full range.

## Diagnostics

Press `?` for a full read-out of the device, connection, and link health:

```
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃                                                                          ┃
┃  diagnostics                                        ● connected · 16:40  ┃
┃                                                                          ┃
┃  host     root@192.0.2.13           uptime   3h 25m                      ┃
┃  device   Arylic AR241CE · LS8      os       Linux 5.15.137              ┃
┃  firmware AR241CE_9243.16           build    2025-12-24 · app 312        ┃
┃  mac      aa:bb:cc:dd:ee:ff         cores    2                           ┃
┃  ───────────────────────────── connection ─────────────────────────────  ┃
┃  player    ssh stream · rx 0.0s ago · 1 attempt                          ┃
┃  control   tunnel :2018 · live                                           ┃
┃  ────────────────────────────── network ───────────────────────────────  ┃
┃  link      ethernet · 100 Mbit/s · full duplex                           ┃
┃  address   192.0.2.13 · gw 192.0.2.1                                     ┃
┃  traffic   rx 1.2 MB/s · tx 45 KB/s                                      ┃
┃  latency   you       11 ms ±6.6  max 31   ▁▂▁█▃▁▂▁▁▂▁█▃▁▂▁▁▂             ┃
┃            gw       6.6 ms ±1.1  max 12   ▁▁▂▁▁▁▂▁▁▁▁▁▂▁▁▁▁▂             ┃
┃            spotify   25 ms ±2.0  max 29   ▂▃▂▂▃▂▂▃▂▂▂▃▂▂▃▂▂▃             ┃
┃  ─────────────────────────────── audio ────────────────────────────────  ┃
┃  format    audio/ogg · 44.1 kHz                                          ┃
┃  volume    ████████░░░░░░░░░░  44%                                       ┃
┃  eq        EQ on · T +3 · M 0 · B +3 · Sub on 15 · Max Vol 100           ┃
┃  ───────────────────────────── resources ──────────────────────────────  ┃
┃  cpu       █████░░░░░░░░░░░░░  26%   1m 0.51 · 5m 0.40 · 15m 0.38        ┃
┃  memory    ███████░░░░░░░░░░░  37%   135 / 215 MB free                   ┃
┃  temp      ███████████░░░░░░░  52 °C   SoC                               ┃
┃  storage   ███░░░░░░░░░░░░░░░  17%   1228 / 7168 MB used · data          ┃
┃                                                                          ┃
┃  live · any key returns to the dashboard                                 ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

The resource gauges and the network stats (throughput, Wi-Fi signal, and the three
ping round-trips) are collected on the device **only while this overlay is open** —
close it and the on-device loop drops back to the bare minimum. Each latency row
keeps a rolling ~30s sparkline and a peak, so an intermittent spike (a powerline
link dropping out, say) is visible after the fact — that window spans only the
current viewing, since nothing is gathered with the overlay closed. The internet-ping
target is the `ping_host` config key (default `spotify.com`). Any key returns to the
dashboard.

## How it works

One direct `ssh` child is the whole transport — no ControlMaster, no expect. A
BusyBox-ash loop on the device streams framed snapshots:

- **Adaptive cadence** — cheap reads roughly once a second while playing,
  stretching to ~3 s when idle. The now-playing JSON is shipped only when it
  changes; the play position is resynced periodically while the UI extrapolates
  it locally between reads; the resource stats run **only while the diagnostics
  overlay is open**. The per-tick work is kept to the minimum of device-API
  reads — every other stat comes from `/proc` and `/sys` via shell builtins.
- **Whitelisted commands** — input to the device is a whitelist of
  `<mid> <data>` lines (transport, volume, and a stats-on/off toggle), never
  `eval`. Failed sends are held and delivered in order on reconnect; stale ones
  are dropped visibly.
- **Keychain auth** — password-only via `SSH_ASKPASS`: the binary re-execs
  itself and answers ssh's prompt from the macOS Keychain.
- **Self-reaping** — the loop detects a dead session by read-timing and exits,
  so both ends are reaped no matter how the TUI died; the client reconnects with
  backoff.

### Security & threat model

> **lp10 is built for a trusted home LAN, and only that.**

- **Host keys are deliberately not verified.** The LP10 regenerates its SSH host
  key on every boot from a ramfs, so pinning is pointless: lp10 runs ssh with
  `StrictHostKeyChecking=no` and `UserKnownHostsFile=/dev/null` (see
  `transport.SSHArgv`). This is the **one intentional security tradeoff** — a
  static analyzer (gosec / CodeQL) will flag it, by design — and it means lp10
  offers **no protection against a man-in-the-middle** on the path to the device.
  Only run it on a network you control.
- **The password never touches the repo.** It lives solely in the macOS Keychain
  and is delivered to ssh through `SSH_ASKPASS`; it is not in the source, git
  history, config files, shell history, or `ps` output.
- **The device is trusted as root.** Commands are a fixed whitelist, never
  `eval`, but lp10 logs in as `root@LP10` — treat the device as you would any
  appliance you have root on.

Do not expose the LP10 to the public internet, and don't run lp10 across an
untrusted network. There is intentionally no transport hardening beyond SSH's
password auth.

## Configuration (optional)

`~/.config/lp10/config.toml` (or `$XDG_CONFIG_HOME/lp10/config.toml`) — defaults shown:

```toml
host      = "lp10.local"    # fallback IP / mDNS name when discovery is off or finds nothing
user      = "root"
name      = "LP10"          # UI label; discovery refines it to "LP10 · <device name>" (also the disambiguation hint)
vol_step  = 2               # volume change per keypress (1–100)
ping_host = "spotify.com"   # diagnostics: the device's internet-latency target
discover  = true            # find the LP10 on the LAN via mDNS at startup
art       = true            # show the real album cover (off => the plasma motif)
art_mode  = "auto"          # auto | kitty | halfblock | off  (see below)
mouse     = true            # click / drag / scroll controls (off keeps native selection)
```

### Album art

The art panel renders the track's `CoverArtUrl`, fetched once and cached under
`~/.local/state/lp10/art/` (so a re-seen cover needs no network and the last
cover paints instantly on the next launch). `art_mode` picks the renderer:

- `auto` *(default)* — **Kitty** true-pixel graphics on a terminal that
  advertises support (Ghostty, kitty), a **half-block** raster on any other
  truecolor terminal, the **plasma motif** otherwise.
- `kitty` — force the Kitty path even when it isn't auto-detected (e.g. WezTerm /
  Konsole, or kitty/Ghostty inside tmux where detection backs off). It only falls
  back if the image can't be encoded; on a terminal that genuinely can't
  composite, use `halfblock`.
- `halfblock` — always the 24-bit half-block raster (no graphics protocol).
- `off` — never fetch, cache, or draw art; keep the plasma motif.

> The Kitty path uses Unicode-placeholder graphics so it composes with the
> diff renderer. If your terminal claims Kitty support but the cover renders
> wrong, set `art_mode = "halfblock"`.

### Discovery

With `discover = true` (the default), lp10 sends a multicast-DNS query at startup
and connects to whichever LP10 answers — so a changed DHCP lease never needs a
config edit. It identifies the device by the `am=LP10` fingerprint the AirPlay
daemon advertises (`_raop._tcp`), reads its current IP, and uses it; the UI is
then labelled with the device's own advertised name (`LP10 · Living`), so nothing
is hardcoded. The query goes out **every** active interface, so a multi-homed Mac
(docked Ethernet, a VPN, or a Wi-Fi you just switched to) still finds a device on
a non-default interface. With more than one LP10, set `name` to the target's
advertised name to pick it (e.g. `name = "Living"`); otherwise the sole/first one
is used. It is pure mDNS — no bound port, no dependency, ~tens of milliseconds
when the device is present, and it falls back to `host` if nothing answers, so
startup never blocks on a missing device. Set `discover = false` to pin `host`
(an IP, or a `.local` name your OS resolves).

`LP10_HOST` overrides `host` for a single run and skips discovery. Persistent state (the pre-mute
level and the now-playing/EQ snapshot used for instant first paint) lives under
`~/.local/state/lp10/`.

## Development

```sh
make test    # go vet + the full suite, fully off-device
make run     # launch the live TUI
```

The suite never touches a real device: `LP10_SSH` swaps in a fake ssh transport
(`cmd/fakessh`) selected by `LP10_FAKE_SCENARIO` (`normal`, `silent`, `dataless`,
`eof`, `garbage`, `authfail`, `keychain-locked`, `heal`), and `LP10_STATE_DIR`
isolates persistent state. The on-device shell loop is checked for validity
(`sh -n`) and its parsers are exercised against captured device output, so edits
to it fail in CI rather than silently on the device.

## Project layout

```
main.go                 entry: askpass hot path, signals, run + teardown
internal/config/        config file, paths, premute/snapshot persistence
internal/protocol/      LUCI wire framing, MB42 parse, command reduction, State
internal/transport/     Keychain/askpass auth, ssh argv, the on-device loop
internal/discovery/     one-shot mDNS query to find the LP10 on the LAN
internal/workers/       stream / command / watchdog / EQ-tunnel / album-art goroutines + teardown
internal/tunnel/        the :2018 plain-text EQ/control protocol
internal/artwork/       album-cover fetch/cache + half-block & Kitty rasterizers
internal/tui/           Bubble Tea model, rendering, input dispatch, helpers
internal/fixtures/      embedded wire-record fixtures (shared by tests + fake)
cmd/fakessh/            fake ssh transport for tests (substituted via LP10_SSH)
internal/testutil/      test helpers (env isolation, fake/binary builders)
internal/e2e/           end-to-end tests (argv contract, pty smoke)
```

## Dependencies

- [`bubbletea`](https://github.com/charmbracelet/bubbletea) / [`lipgloss`](https://github.com/charmbracelet/lipgloss) / [`termenv`](https://github.com/muesli/termenv) — terminal UI
- [`BurntSushi/toml`](https://github.com/BurntSushi/toml) — config
- [`mattn/go-runewidth`](https://github.com/mattn/go-runewidth) / [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) — display width
- [`creack/pty`](https://github.com/creack/pty) — pty smoke test only

## License

MIT — see [LICENSE](LICENSE).
