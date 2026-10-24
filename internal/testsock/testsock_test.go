package testsock

import (
	"net"
	"strings"
	"testing"
)

func TestPathBindsWhateverTheTempDirAndTestNameLengths(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp/"+strings.Repeat("d", 120))
	t.Run(strings.Repeat("a_very_long_subtest_name_", 6), func(t *testing.T) {
		p := Path(t, "treeman.sock")
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatalf("bind %s (%d bytes): %v", p, len(p), err)
		}
		_ = ln.Close()
	})
}

func TestFitsRejectsADirectoryWithoutRoomForTheName(t *testing.T) {
	dir := "/tmp/" + strings.Repeat("x", 80)
	err := fits(dir, maxBasenameBytes)
	if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "sun_path") {
		t.Fatalf("fits(%q) = %v, want an error naming the directory and the sun_path limit", dir, err)
	}
	if err := fits("/tmp/tm-123", maxBasenameBytes); err != nil {
		t.Fatalf("a short directory must fit: %v", err)
	}
}
