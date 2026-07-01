//go:build !darwin

package mediakey

// Start is a no-op on platforms without a supported media-key tap. It returns a
// no-op stop function and a nil error, so callers need no build-tag handling.
func Start(Config) (stop func(), err error) {
	return func() {}, nil
}
