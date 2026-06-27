//go:build linux

package e2e

// Per-OS stub for the Linux secret store (secret-tool) driven by
// TestAskpassIntegration. secret-tool signals a missing item by a non-zero exit
// with no stderr; a locked keyring (or D-Bus failure) writes a diagnostic to
// stderr, which we treat as locked.
const (
	askpassStubBin    = "secret-tool"
	askpassNoItemStub = "exit 1\n"
	askpassLockedStub = "echo 'Cannot create an item in a locked collection' 1>&2\nexit 1\n"
)
