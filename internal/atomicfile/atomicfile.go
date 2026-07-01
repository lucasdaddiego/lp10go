// Package atomicfile writes small files via a deterministic .tmp sibling and an
// atomic rename, so a reader never observes a half-written file and a writer
// frozen mid-write leaves exactly one well-known temp path, overwritten on the
// next attempt. Shared by config (the premute/snapshot state) and artwork (the
// cover cache); both treat persistence as best-effort and ignore the error.
package atomicfile

import "os"

// Write writes data to path via path+".tmp" then a rename. Files are created
// 0600 (every caller persists private per-user state). On any failure the temp
// file is removed and an existing target is left untouched.
func Write(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
