//go:build !darwin

package mediakey

import "testing"

// On non-darwin, Start is a no-op that must still return a usable stop func and a
// nil error.
func TestStartNoop(t *testing.T) {
	stop, err := Start(Config{})
	if err != nil {
		t.Fatalf("Start err = %v, want nil", err)
	}
	if stop == nil {
		t.Fatal("stop func is nil")
	}
	stop() // must not panic
}
