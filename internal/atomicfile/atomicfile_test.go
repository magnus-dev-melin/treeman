package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteKeepsModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.env")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".env")
	if err := os.Symlink("real.env", link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file: %v", err)
	}
	got, _ := os.ReadFile(target)
	fi, _ := os.Stat(target)
	if string(got) != "new" || fi.Mode().Perm() != 0o640 {
		t.Errorf("target = %q mode %v, want \"new\" mode 0640", got, fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestWriteNewFileUsesPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := Write(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteRefusesDanglingSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "x")
	if err := os.Symlink("missing", link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("x"), 0o600); err == nil {
		t.Fatal("dangling symlink must be refused")
	}
}
