//go:build linux

package transport

import "strings"

// StoreHint is the one-time command that saves the LP10 password into the Secret
// Service (GNOME Keyring / KWallet) through libsecret. secret-tool reads the
// secret from an interactive prompt — never the command line — so it stays out of
// shell history and `ps` output. Needs libsecret-tools and a running keyring.
const StoreHint = "secret-tool store --label=lp10 service lp10 account root"

// secretToolName and secretStoreName name the backend in the user-facing askpass
// error messages (see ClassifyStderr).
const (
	secretToolName  = "secret-tool"
	secretStoreName = "The Secret Service keyring"
)

// secretLookupArgv prints the saved password to stdout, non-interactively.
func secretLookupArgv() []string {
	return []string{"secret-tool", "lookup", "service", "lp10", "account", "root"}
}

// secretNotFound reports the no-such-item case (consulted only when the lookup
// exited non-zero): secret-tool exits non-zero with nothing on stderr. A locked
// keyring or a D-Bus failure writes a diagnostic there instead, which we treat as
// "locked" rather than "no item".
func secretNotFound(o secOutcome) bool {
	return strings.TrimSpace(o.stderr) == ""
}
