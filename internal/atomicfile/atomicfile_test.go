package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSuccessRemovesTmp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	if err := Write(path, []byte("test content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "test content" {
		t.Errorf("content = %q, err %v; want the written data", b, err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error(".tmp should be gone after success")
	}
}

func TestWriteFailedCreateLeavesNoTmp(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "nonexistent", "test.txt")
	if err := Write(badPath, []byte("test")); err == nil {
		t.Error("Write into a missing parent should report an error")
	}
	if _, err := os.Stat(badPath + ".tmp"); err == nil {
		t.Error(".tmp should not linger after a failed write")
	}
}

// Renaming the .tmp file onto an existing directory fails, the .tmp sibling is
// cleaned up, and the target is left untouched.
func TestWriteRenameOntoDirFailsAndCleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, []byte("data")); err == nil {
		t.Error("rename onto a directory should report an error")
	}
	if _, err := os.Stat(dir + ".tmp"); err == nil {
		t.Error(".tmp should be removed after a failed rename")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Error("target should still be a directory after the failed write")
	}
}
