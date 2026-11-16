package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteKeepsModeAndCleansUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteNewFileUsesPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := Write(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
}

func TestWriteMissingDir(t *testing.T) {
	if err := Write(filepath.Join(t.TempDir(), "no", "f"), nil, 0o644); err == nil {
		t.Error("Write into a missing directory succeeded")
	}
}
