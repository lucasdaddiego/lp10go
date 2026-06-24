# lp10

> One command, one screen — a terminal player and equalizer for the **Arylic LP10**
> network audio streamer, driven over a single SSH connection.

![license](https://img.shields.io/badge/license-MIT-blue)
![go](https://img.shields.io/badge/go-1.24%2B-00ADD8)
![platform](https://img.shields.io/badge/platform-macOS-lightgrey)

```
$ lp10
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃                                                                            ┃
┃  ♪ LP10 · Living  ● connected · 16:57                               Vol    ┃
┃                                                                            ┃
┃  ████████████  De Música Ligera                                      █     ┃
┃  ████████████  Soda Stereo · Canción Animal                          █     ┃
┃  ████████████  Spotify · audio/ogg · 44.1 kHz                        █     ┃
┃  ████████████                                                        █     ┃
┃  ████████████  ▶ 00:30 ━━━━●───────────────────────────── -03:01     █     ┃
┃  ████████████         ◀◀            ⏸ pause            ▶▶           92%    ┃
┃                                                                            ┃
┃  ────────────────────────────── equalizer ───────────────────────────────  ┃
┃                                                                            ┃
┃      EQ      Treble     Mid      Bass    ┃    Sub      Lvl    ┃  Max Vol   ┃
┃                                          ┃                    ┃            ┃
┃      ●         ░         ░         ░     ┃     ●        ░     ┃     █      ┃
┃      ┃         ░         ░         ░     ┃     ┃        ░     ┃     █      ┃
┃      ┃         █         █         █     ┃     ┃        ░     ┃     █      ┃
┃      ┃         █         █         █     ┃     ┃        ░     ┃     █      ┃
┃      ┃         █         █         █     ┃     ┃        █     ┃     █      ┃
┃      on        +3        0        +3     ┃    on       10     ┃    100     ┃
┃                                          ┃                    ┃            ┃
┃                 space play · ↑↓ vol · m mute · e/tab EQ · ? diag · q quit  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

`lp10` turns the Arylic LP10 (a LibreWireless / LUCI network streamer) into a
live terminal dashboard — now-playing, transport, a graphic equalizer, and a
diagnostics overlay — from a single statically-linked Go binary. No companion
app, no browser, no background daemon: run `lp10`, get one screen.

## Features

- **Live now-playing** — title, artist · album, source / quality, an animated
  album motif, a seek bar, and segmented transport buttons.
- **Graphic equalizer** — the EQ switch and treble / mid / bass tone, a deep-bass
  switch and level, and the output cap (Max Volume) — driven over the device's
  own control channel. Paints instantly from a cached snapshot on launch.
- **Diagnostics overlay** (`?`) — device and firmware, the active network link
  (Wi-Fi or ethernet, with live throughput and round-trip latency — jitter, peak,
  and a rolling sparkline — to your laptop, the gateway, and the internet), audio,
  and resource gauges (cpu · memory · temp · storage), gathered on the device
  **only while the overlay is open**.
- **Finds the device itself** — mDNS auto-discovery at startup locates the LP10 on
  the LAN by its `am=LP10` advertisement, so a changed DHCP lease never needs a
  config edit. Pure mDNS (no dependency, no bound port); falls back to the
  configured host.
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

# 2. Build and install.
go build -o lp10 .
ln -sf "$PWD/lp10" ~/.bin/lp10        # or anywhere on your PATH

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
| `↑` / `↓` | player: volume ± step · equalizer: adjust the focused band |
| `←` / `→` | player: move button focus · equalizer: pick a band |
| `enter` | player: press the focused button · equalizer: toggle an on/off band |
| `tab` | switch pane (player ↔ equalizer) |
| `e` | jump focus to the equalizer |
| `m` | mute (volume 0 ↔ restored level, persisted) |
| `t` | right-hand time: remaining ↔ total |
| `?` | diagnostics overlay (see below) |
| `q` | quit |

The view adapts to the terminal size: the full **dashboard** (now-playing with
album-motif art, a vertical volume slider, and the graphic equalizer) at ≥ 25
rows / 70 cols, a **compact** frame (no art, inline volume, one-line EQ summary)
below that, and a one-line **mini** view below 9 rows / 58 cols.

## Equalizer

The equalizer pane (focus it with `e` or `tab`) drives the device's tone and
output as a graphic EQ, in three groups — the **EQ** switch and **Treble / Mid /
Bass** tone │ the deep-bass **Sub** switch and its **Lvl** │ **Max Vol**, the
output cap, kept last as it's rarely touched.

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
┃  host     root@192.168.1.13         uptime   3h 25m                      ┃
┃  device   Arylic AR241CE · LS8      os       Linux 5.15.137              ┃
┃  firmware AR241CE_9243.16           build    2025-12-24 · app 312        ┃
┃  mac      aa:bb:cc:dd:ee:ff         cores    2                           ┃
┃  ───────────────────────────── connection ─────────────────────────────  ┃
┃  player    ssh stream · rx 0.0s ago · 1 attempt                          ┃
┃  control   tunnel :2018 · live                                           ┃
┃  ────────────────────────────── network ───────────────────────────────  ┃
┃  link      ethernet · 100 Mbit/s · full duplex                           ┃
┃  address   192.168.1.13 · gw 192.168.1.1                                 ┃
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
  key on every boot from a ramfs, so pinning is pointless; `StrictHostKeyChecking`
  is off by design. This means lp10 offers **no protection against a
  man-in-the-middle** on the path to the device — only run it on a network you
  control.
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

`~/.config/lp10/config.toml` — defaults shown:

```toml
host      = "lp10.local"    # fallback IP / mDNS name when discovery is off or finds nothing
user      = "root"
name      = "LP10 · Living" # header label; its tail also disambiguates discovery (see below)
vol_step  = 2               # volume change per keypress (1–100)
ping_host = "spotify.com"   # diagnostics: the device's internet-latency target
discover  = true            # find the LP10 on the LAN via mDNS at startup
```

### Discovery

With `discover = true` (the default), lp10 sends one multicast-DNS query at
startup and connects to whichever LP10 answers — so a changed DHCP lease never
needs a config edit. It identifies the device by the `am=LP10` fingerprint the
AirPlay daemon advertises (`_raop._tcp`), reads its current IP, and uses it. If
you have more than one LP10, the device whose advertised name appears in `name`
wins (e.g. `name = "LP10 · Living"` picks the one called *Living*); otherwise the
sole/first one is used. It is pure mDNS — no bound port, no dependency, ~tens of
milliseconds when the device is present, and it falls back to `host` if nothing
answers, so startup never blocks on a missing device. Set `discover = false` to
pin `host` (an IP, or a `.local` name your OS resolves).

`LP10_HOST` overrides `host` for a single run and skips discovery. Persistent state (the pre-mute
level and the now-playing/EQ snapshot used for instant first paint) lives under
`~/.local/state/lp10/`.

## Development

```sh
go test ./...        # the full suite, fully off-device
go vet ./...
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
internal/workers/       stream / command / watchdog goroutines + teardown
internal/tunnel/        the :2018 plain-text EQ/control protocol
internal/tui/           Bubble Tea model, rendering, input dispatch, helpers
internal/fixtures/      embedded wire-record fixtures (shared by tests + fake)
cmd/fakessh/            fake ssh transport for tests (substituted via LP10_SSH)
internal/testutil/      test helpers (env isolation, fake/binary builders)
internal/e2e/           end-to-end tests (argv contract, pty smoke)
```

## Dependencies

- [`bubbletea`](https://github.com/charmbracelet/bubbletea) / [`lipgloss`](https://github.com/charmbracelet/lipgloss) — terminal UI
- [`BurntSushi/toml`](https://github.com/BurntSushi/toml) — config
- [`mattn/go-runewidth`](https://github.com/mattn/go-runewidth) / [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) — display width
- [`creack/pty`](https://github.com/creack/pty) — pty smoke test only

## License

MIT — see [LICENSE](LICENSE).
