package procuse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func startSleeper(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func holderOf(scan Scan, pid int) (Holder, bool) {
	for _, h := range scan.Holders {
		if h.PID == pid {
			return h, true
		}
	}
	return Holder{}, false
}

func TestUnderFindsProcessWorkingInsideTree(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "vendor", "bin")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := startSleeper(t, nested)

	scan, err := Under(context.Background(), root)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	h, ok := holderOf(scan, pid)
	if !ok {
		t.Fatalf("sleeper pid %d with cwd %s not reported; holders=%v", pid, nested, scan.Holders)
	}
	if h.Use != UseCwd {
		t.Errorf("use = %s, want %s", h.Use, UseCwd)
	}
	if !scan.Descends(pid, os.Getpid()) {
		t.Errorf("sleeper %d should descend from the test process %d", pid, os.Getpid())
	}
}

func TestUnderIgnoresSiblingWithSharedPrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wt")
	sibling := filepath.Join(parent, "wt-other")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pid := startSleeper(t, sibling)

	scan, err := Under(context.Background(), root)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	if h, ok := holderOf(scan, pid); ok {
		t.Fatalf("process in sibling dir %s reported as holder of %s: %v", sibling, root, h)
	}
}

func TestUnderFindsOpenFile(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "dep.phar"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	scan, err := Under(context.Background(), root)
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	h, ok := holderOf(scan, os.Getpid())
	if !ok {
		t.Fatalf("test process holding %s open not reported; holders=%v", f.Name(), scan.Holders)
	}
	if h.Use != UseOpenFile {
		t.Errorf("use = %s, want %s", h.Use, UseOpenFile)
	}
}

func TestUnderMissingDirectoryHasNoHolders(t *testing.T) {
	scan, err := Under(context.Background(), filepath.Join(t.TempDir(), "gone"))
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	if len(scan.Holders) != 0 {
		t.Fatalf("holders of a missing dir = %v, want none", scan.Holders)
	}
}

func TestDescends(t *testing.T) {
	scan := Scan{parents: map[int]int{10: 1, 20: 10, 30: 20, 1: 0, 40: 40}}
	cases := []struct {
		pid, ancestor int
		want          bool
	}{
		{30, 30, true},
		{30, 20, true},
		{30, 1, true},
		{20, 30, false},
		{40, 1, false},
		{99, 1, false},
	}
	for _, c := range cases {
		if got := scan.Descends(c.pid, c.ancestor); got != c.want {
			t.Errorf("Descends(%d, %d) = %v, want %v", c.pid, c.ancestor, got, c.want)
		}
	}
}
