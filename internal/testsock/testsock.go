package testsock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/sunpath"
)

const (
	base             = "/tmp"
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
	if n := len(dir) + 1 + nameBytes; n > sunpath.Max {
		return fmt.Errorf("unix socket directory %q leaves %d bytes for a %d-byte socket name; "+
			"this platform's sun_path limit is %d bytes", dir, sunpath.Max-len(dir)-1, nameBytes, sunpath.Max)
	}
	return nil
}
