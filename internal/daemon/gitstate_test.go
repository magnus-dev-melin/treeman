package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGitOpInProgress covers each conflict-prone marker (files and the
// rebase directories) plus the clean case.
func TestGitOpInProgress(t *testing.T) {
	cases := []struct {
		marker string
		isDir  bool
		want   string
	}{
		{"MERGE_HEAD", false, "merge"},
		{"rebase-merge", true, "rebase"},
		{"rebase-apply", true, "rebase"},
		{"CHERRY_PICK_HEAD", false, "cherry-pick"},
		{"REVERT_HEAD", false, "revert"},
	}
	for _, c := range cases {
		t.Run(c.marker, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, c.marker)
			if c.isDir {
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := gitOpInProgress(dir); got != c.want {
				t.Errorf("gitOpInProgress = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("clean", func(t *testing.T) {
		if got := gitOpInProgress(t.TempDir()); got != "" {
			t.Errorf("gitOpInProgress(clean) = %q, want \"\"", got)
		}
	})
}

// TestIsGitOpMarker guards the watcher's event-filter widening.
func TestIsGitOpMarker(t *testing.T) {
	for _, base := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if !isGitOpMarker(base) {
			t.Errorf("isGitOpMarker(%q) = false, want true", base)
		}
	}
	for _, base := range []string{"HEAD", "ORIG_HEAD", "index", "config"} {
		if isGitOpMarker(base) {
			t.Errorf("isGitOpMarker(%q) = true, want false", base)
		}
	}
}

func TestAwaitIndexUnlocked(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	gitRun(t, "", "init", "-q", "-b", "main", repo)
	lock := filepath.Join(repo, ".git", "index.lock")

	if err := awaitIndexUnlocked(t.Context(), repo, time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("no lock: %v", err)
	}

	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	released := time.AfterFunc(150*time.Millisecond, func() { _ = os.Remove(lock) })
	defer released.Stop()
	start := time.Now()
	if err := awaitIndexUnlocked(t.Context(), repo, 5*time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("released lock: %v", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Fatalf("returned after %s, before git released the lock", waited)
	}

	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := awaitIndexUnlocked(t.Context(), repo, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), lock) {
		t.Fatalf("stale lock: err = %v, want a timeout naming %s", err, lock)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitIndexUnlocked(ctx, repo, 5*time.Second, 10*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: err = %v, want context.Canceled", err)
	}
}
