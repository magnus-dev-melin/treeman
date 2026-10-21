package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func addFakeLinkedWorktree(t *testing.T, repoPath, name string) string {
	t.Helper()
	wtPath := filepath.Join(repoPath, ".worktrees", name)
	adminDir := filepath.Join(repoPath, ".git", "worktrees", name)
	for _, dir := range []string{wtPath, adminDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adminDir, "gitdir"), []byte(wtPath+"/.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return wtPath
}

func waitForActiveWorktreeRow(t *testing.T, st *State, wtPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row, err := st.Store.LookupActiveWorktreeByPath(t.Context(), wtPath); err == nil && row.ID != 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lifecycle watcher never registered %s", wtPath)
}

func startTestLifecycleWatcher(t *testing.T, st *State, repoPath string) {
	t.Helper()
	repoID, err := st.Store.EnsureRepo(t.Context(), repoPath, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureLifecycleWatcher(t.Context(), st, repoID, repoPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.UnregisterLifecycleWatcher(repoPath) })
}

func TestLifecycleDetectsAddAfterWorktreesDirRecreated(t *testing.T) {
	st := newTestState(t)
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	startTestLifecycleWatcher(t, st, repoPath)

	if err := os.Remove(filepath.Join(repoPath, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	waitForActiveWorktreeRow(t, st, addFakeLinkedWorktree(t, repoPath, "feat"))
}

func TestLifecycleDetectsAddWhenWorktreesDirMissingAtStart(t *testing.T) {
	st := newTestState(t)
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	startTestLifecycleWatcher(t, st, repoPath)

	waitForActiveWorktreeRow(t, st, addFakeLinkedWorktree(t, repoPath, "feat"))
}

func TestEnsureLifecycleWatcherKeepsTheRunningWatcher(t *testing.T) {
	st := newTestState(t)
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	startTestLifecycleWatcher(t, st, repoPath)
	st.mu.Lock()
	first := st.lifecycleWatchers[repoPath]
	st.mu.Unlock()
	startTestLifecycleWatcher(t, st, repoPath)
	st.mu.Lock()
	second := st.lifecycleWatchers[repoPath]
	st.mu.Unlock()
	if second != first {
		t.Fatal("a second ensure replaced the running lifecycle watcher")
	}
	waitForActiveWorktreeRow(t, st, addFakeLinkedWorktree(t, repoPath, "feat"))
}
