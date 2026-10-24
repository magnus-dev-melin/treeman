package testsock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const (
	base             = "/tmp"
	maxPathBytes     = 103
	maxBasenameBytes = 32
)

func NewDir() (string, error) {
	dir, err := os.MkdirTemp(base, "tm-")
	if err != nil {
		return "", fmt.Errorf("create a unix socket directory under %s: %w", base, err)
	}
	if err := fits(dir, maxBasenameBytes); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func Dir(tb testing.TB) string {
	tb.Helper()
	dir, err := NewDir()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func Path(tb testing.TB, name string) string {
	tb.Helper()
	if len(name) > maxBasenameBytes {
		tb.Fatalf("unix socket name %q is %d bytes; socket names must be at most %d bytes", name, len(name), maxBasenameBytes)
	}
	return filepath.Join(Dir(tb), name)
}

func fits(dir string, nameBytes int) error {
	if n := len(dir) + 1 + nameBytes; n > maxPathBytes {
		return fmt.Errorf("unix socket directory %q leaves %d bytes for a %d-byte socket name; "+
			"sun_path holds at most %d bytes (104 on macOS, 108 on Linux, minus the terminating NUL)",
			dir, maxPathBytes-len(dir)-1, nameBytes, maxPathBytes)
	}
	return nil
}
