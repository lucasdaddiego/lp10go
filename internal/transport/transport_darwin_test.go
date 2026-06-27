//go:build darwin

package transport

import (
	"strings"
	"testing"
)

// security(1) reports a missing item by exiting non-zero with "could not be
// found" on stderr -> no-item.
func TestKeychainPasswordNoItem(t *testing.T) {
	withRunSecurity(t, func() secOutcome {
		return secOutcome{rc: 44, stderr: "security: ... could not be found."}
	})
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerNoItem) {
		t.Fatalf("err = %v, want %s", err, MarkerNoItem)
	}
}

// Any other non-zero exit (e.g. a locked Keychain) -> locked, not no-item.
func TestKeychainPasswordOtherFailureMeansLocked(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{rc: 51, stderr: "some other error"} })
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerLocked) {
		t.Fatalf("err = %v, want %s", err, MarkerLocked)
	}
}

// The store hint must prompt interactively (-w, no password in argv).
func TestStoreHintIsInteractiveSecurityCommand(t *testing.T) {
	if !strings.Contains(StoreHint, "add-generic-password") {
		t.Errorf("darwin hint should use security add-generic-password, got %q", StoreHint)
	}
	if !strings.HasSuffix(strings.TrimRight(StoreHint, " "), "-w") {
		t.Error("hint must end with -w so security(1) prompts (no password in argv)")
	}
}
