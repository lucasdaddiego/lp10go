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
// Config: ~/.config/lp10/config.toml (optional) — host, user, name, vol_step.
// LP10_HOST overrides host for one run. State: ~/.local/state/lp10/.
// First-run: security add-generic-password -U -a root -s lp10 -w
package main

import (
	"fmt"
	"os"

	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/transport"
	"github.com/lucasdaddiego/lp10go/internal/tui"
)

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

	// tui.Run handles SIGTERM/SIGHUP and Ctrl-C cooperatively and returns the
	// exit code (0 clean, 130 interrupt, 143 signal) after running teardown and
	// restoring the terminal.
	code, err := tui.Run(config.Load())
	if err != nil {
		fmt.Fprintf(os.Stderr, "lp10: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}
