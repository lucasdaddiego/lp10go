// Command lp10 is a terminal player for the Arylic LP10 (LibreWireless LUCI
// over SSH). Run `lp10` (no arguments) for the live TUI; there are no other
// commands.
//
// Transport: ONE direct ssh connection to root@LP10 (password from the macOS
// Keychain item service=lp10 account=root, delivered via SSH_ASKPASS self-exec).
// The remote shell loop streams state snapshots and evals nothing: its stdin
// accepts whitelisted `<mid> <data>` lines only. When this process dies — however
// it dies — ssh exits, the session closes, and the loop EOF-exits within ~1 s.
// Host keys are deliberately not verified (LAN device, ramfs host keys).
//
// Config: ~/.config/lp10/config.toml (optional) — host, user, name, vol_step,
// ping_host, discover. Unless discover=false or LP10_HOST is set, a startup mDNS
// query finds the LP10 on the LAN (am=LP10) and uses its current address, with
// host as the fallback. State: ~/.local/state/lp10/.
// First-run: security add-generic-password -U -a root -s lp10 -w
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/discovery"
	"github.com/lucasdaddiego/lp10go/internal/transport"
	"github.com/lucasdaddiego/lp10go/internal/tui"
)

// discoverTimeout bounds the startup mDNS probe. A present device answers in well
// under this — the first reply early-exits (~tens of ms), and discovery.FindLP10
// retransmits within the window to ride out UDP loss — so it only bites as a brief
// delay before the cached first paint when nothing is on the LAN. Kept short for
// that reason; the configured host is the fallback.
const discoverTimeout = 1 * time.Second

const usage = "lp10: takes no arguments — run `lp10` for the live TUI"

func main() {
	// Askpass hot path first: ssh re-execs this binary as SSH_ASKPASS on every
	// connection attempt, so it must stay cheap and run before anything else.
	if os.Getenv(transport.AskpassEnv) == "1" {
		transport.AskpassMain() // exits the process
		return
	}

	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cfg := config.Load()
	// Best-effort mDNS discovery so a changed DHCP lease never needs a config
	// edit: find the LP10 on the LAN and use its current address. Pinning the
	// host (LP10_HOST) or `discover = false` skips it; the configured host is the
	// fallback when nothing answers, so startup never blocks on a missing device.
	if cfg.Discover && os.Getenv(config.HostEnv) == "" {
		if dev, ok := discovery.FindLP10(cfg.Name, discoverTimeout); ok {
			cfg.Host, cfg.Discovered = dev.Addr(), true
		}
	}

	// tui.Run handles SIGTERM/SIGHUP and Ctrl-C cooperatively and returns the
	// exit code (0 clean, 130 interrupt, 143 signal) after running teardown and
	// restoring the terminal.
	code, err := tui.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lp10: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}
