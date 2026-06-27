//go:build darwin

package e2e

// Per-OS stub for the macOS secret store (security(1)) driven by
// TestAskpassIntegration. A missing item is signalled by a non-zero exit with
// "could not be found" on stderr; any other non-zero exit means locked.
const (
	askpassStubBin    = "security"
	askpassNoItemStub = "echo 'security: ... could not be found.' 1>&2\nexit 44\n"
	askpassLockedStub = "echo 'User interaction is not allowed.' 1>&2\nexit 1\n"
)
