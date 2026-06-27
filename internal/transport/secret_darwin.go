//go:build darwin

package transport

import "strings"

// StoreHint is the one-time command that saves the LP10 password into the macOS
// login Keychain. -w with no value makes security(1) prompt interactively, so the
// password never lands in shell history or `ps` output.
const StoreHint = "security add-generic-password -U -a root -s lp10 -w"

// secretToolName and secretStoreName name the backend in the user-facing askpass
// error messages (see ClassifyStderr).
const (
	secretToolName  = "security(1)"
	secretStoreName = "The login Keychain"
)

// secretLookupArgv prints the saved password to stdout (-w), non-interactively.
func secretLookupArgv() []string {
	return []string{"security", "find-generic-password", "-a", "root", "-s", "lp10", "-w"}
}

// secretNotFound reports the no-such-item case (consulted only when the lookup
// exited non-zero): security(1) writes "could not be found" to stderr.
func secretNotFound(o secOutcome) bool {
	return strings.Contains(o.stderr, "could not be found")
}
