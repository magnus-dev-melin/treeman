package wt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/testsock"
)

func TestDeleteRefusesWorktreeInUse(t *testing.T) {
	withTempStore(t)
	t.Setenv("TREEMAN_SOCKET", testsock.Path(t, "no-daemon.sock"))
	repoRoot := t.TempDir()
	wtPath := filepath.Join(repoRoot, ".worktrees", "release")
	vendorBin := filepath.Join(wtPath, "vendor", "bin")
	if err := os.MkdirAll(vendorBin, 0o755); err != nil {
		t.Fatal(err)
	}
	deploy := exec.CommandContext(t.Context(), "sleep", "60")
	deploy.Dir = vendorBin
	if err := deploy.Start(); err != nil {
		t.Fatalf("start process inside worktree: %v", err)
	}
	t.Cleanup(func() {
		_ = deploy.Process.Kill()
		_ = deploy.Wait()
	})

	_, err := Delete(context.Background(), DeleteRequest{
		RepoRoot: repoRoot,
		Target:   wtPath,
		Force:    true,
		Detached: true,
	}, NoopSink{})

	var inUse *InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("delete of a worktree a process is running in must be refused with InUseError, got %v", err)
	}
	if len(inUse.Holders) != 1 || inUse.Holders[0].PID != deploy.Process.Pid {
		t.Fatalf("holders = %v, want only pid %d", inUse.Holders, deploy.Process.Pid)
	}
	if _, statErr := os.Stat(vendorBin); statErr != nil {
		t.Fatalf("refused delete must leave the worktree on disk: %v", statErr)
	}
}

func TestEnsureNotInUseIgnoresTheCallingProcess(t *testing.T) {
	t.Setenv("TREEMAN_SOCKET", testsock.Path(t, "no-daemon.sock"))
	wtPath := t.TempDir()
	f, err := os.Create(filepath.Join(wtPath, "composer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if err := EnsureNotInUse(context.Background(), wtPath); err != nil {
		t.Fatalf("the deleting process itself must not count as a holder: %v", err)
	}
}
