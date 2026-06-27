//go:build linux

package transport

import (
	"strings"
	"testing"
)

// secret-tool reports a missing item by exiting non-zero with nothing on stderr
// -> no-item.
func TestKeychainPasswordNoItemExitNonzeroNoStderr(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{rc: 1} })
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerNoItem) {
		t.Fatalf("err = %v, want %s", err, MarkerNoItem)
	}
}

// Older secret-tool exits 0 with empty stdout when the item is absent -> no-item.
func TestKeychainPasswordNoItemExitZeroEmpty(t *testing.T) {
	withRunSecurity(t, func() secOutcome { return secOutcome{rc: 0, stdout: ""} })
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerNoItem) {
		t.Fatalf("err = %v, want %s (secret-tool may exit 0 with empty output)", err, MarkerNoItem)
	}
}

// A non-zero exit WITH a diagnostic on stderr (locked collection / no D-Bus) ->
// locked, not no-item.
func TestKeychainPasswordDBusErrorMeansLocked(t *testing.T) {
	withRunSecurity(t, func() secOutcome {
		return secOutcome{rc: 1, stderr: "Cannot create an item in a locked collection"}
	})
	_, err := KeychainPassword()
	if err == nil || !strings.Contains(err.Error(), MarkerLocked) {
		t.Fatalf("err = %v, want %s", err, MarkerLocked)
	}
}

// The store hint must prompt interactively and never embed the secret in argv.
func TestStoreHintIsInteractiveSecretToolCommand(t *testing.T) {
	if !strings.HasPrefix(StoreHint, "secret-tool store") {
		t.Errorf("linux hint should use secret-tool store, got %q", StoreHint)
	}
	if strings.Contains(StoreHint, "--password") {
		t.Error("hint must not pass --password (would embed the secret in argv)")
	}
	if !strings.Contains(StoreHint, "service lp10 account root") {
		t.Error("store attributes must match the lookup (service lp10 account root)")
	}
}
