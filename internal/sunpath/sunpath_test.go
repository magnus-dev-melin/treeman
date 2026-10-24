package sunpath

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func socketPathOfLength(t *testing.T, n int) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sp-") //nolint:usetesting // t.TempDir embeds the test name and cannot hold a path at the limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, strings.Repeat("s", n-len(dir)-1))
}

func TestMaxIsTheLongestPathThePlatformBinds(t *testing.T) {
	longest := socketPathOfLength(t, Max)
	if err := Check(longest); err != nil {
		t.Fatalf("Check rejected a %d-byte path: %v", len(longest), err)
	}
	ln, err := net.Listen("unix", longest)
	if err != nil {
		t.Fatalf("bind a %d-byte path (the advertised limit): %v", len(longest), err)
	}
	_ = ln.Close()

	over := socketPathOfLength(t, Max+1)
	if _, err := net.Listen("unix", over); err == nil {
		t.Fatalf("bind of a %d-byte path succeeded; Max=%d understates the limit", len(over), Max)
	}
	err = Check(over)
	var tooLong *TooLongError
	if !errors.As(err, &tooLong) || tooLong.Path != over {
		t.Fatalf("Check(%d-byte path) = %v, want a TooLongError for it", len(over), err)
	}
	for _, want := range []string{over, strconv.Itoa(len(over)) + " bytes", strconv.Itoa(Max) + "-byte"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
