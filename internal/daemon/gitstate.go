package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// gitOpMarkers are the entries in a worktree's git directory that
// signal an in-progress, conflict-prone git operation. They live
// alongside HEAD, so the HEAD watcher (which already subscribes to that
// directory) reacts to fsnotify events on them, and gitOpInProgress
// probes them to decide whether a prepare/finalize should be deferred
// until the tree settles. `rebase-merge`/`rebase-apply` are
// directories; the rest are files — os.Stat treats both uniformly.
var gitOpMarkers = map[string]string{
	"MERGE_HEAD":       "merge",
	"rebase-merge":     "rebase",
	"rebase-apply":     "rebase",
	"CHERRY_PICK_HEAD": "cherry-pick",
	"REVERT_HEAD":      "revert",
}

// isGitOpMarker reports whether base names one of the op-state markers.
// The HEAD watcher uses this to widen its event filter beyond HEAD
// itself so it observes the merge/rebase start AND clear transitions.
func isGitOpMarker(base string) bool {
	_, ok := gitOpMarkers[base]
	return ok
}

// resolveGitDir returns the per-worktree git directory — where HEAD,
// MERGE_HEAD, rebase-*/ etc. live. For a linked worktree this is the
// admin dir under <common>/worktrees/<name>/ (NOT the repo's shared
// common dir), which is exactly where git records merge/rebase state
// per worktree. Reuses resolveHeadPath's gitlink handling: the git dir
// is the parent of the resolved HEAD path.
func resolveGitDir(worktreePath string) (string, error) {
	headPath, err := resolveHeadPath(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(headPath), nil
}

// gitOpInProgress reports the in-progress conflict-prone git operation
// in gitDir ("merge"|"rebase"|"cherry-pick"|"revert"), or "" when the
// tree is clean. A worktree mid-merge/-rebase carries conflict markers
// in tracked files, so prepare must defer until the marker clears.
func gitOpInProgress(gitDir string) string {
	for marker, op := range gitOpMarkers {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return op
		}
	}
	return ""
}

// worktreeGitOp is the worktree-path convenience used by the finalize
// guards: resolve the git dir, then probe. Returns "" (proceed) when
// the git dir can't be resolved — a missing/corrupt .git pointer is the
// watcher's problem to surface, not a reason to silently defer prepare
// forever.
func worktreeGitOp(worktreePath string) string {
	gitDir, err := resolveGitDir(worktreePath)
	if err != nil {
		return ""
	}
	return gitOpInProgress(gitDir)
}

// indexLockWait bounds how long a finalize waits for git to release a
// worktree's index lock. A merge or checkout of a large tree holds it
// for seconds; one held longer is a stale lock from a git that died,
// which git itself refuses to work past either.
const indexLockWait = 2 * time.Minute

// indexLockPoll is how often awaitIndexUnlocked re-checks the lock.
const indexLockPoll = 100 * time.Millisecond

// awaitIndexUnlocked blocks until no git process holds wtRoot's index
// lock (`<gitdir>/index.lock`). A merge or checkout holds it for the
// whole time it rewrites the tracked files, and a clean merge leaves
// no other marker (MERGE_HEAD only exists when it stops on a
// conflict). Reading the config or inputs mid-rewrite sees a half-
// updated tree: a moved dump whose new path is in treeman.yaml but not
// yet on disk (#173). Returns an error naming the lock when it is
// still held after `timeout`, or ctx's error when ctx ends first.
// Proceeds when the git dir can't be resolved, like worktreeGitOp.
func awaitIndexUnlocked(ctx context.Context, wtRoot string, timeout, poll time.Duration) error {
	gitDir, err := resolveGitDir(wtRoot)
	if err != nil {
		return nil //nolint:nilerr // an unresolvable git dir is the watcher's to surface, not a reason to block
	}
	lock := filepath.Join(gitDir, "index.lock")
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(lock); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("git has held %s for over %s; if no git command is running there, "+
				"remove the stale lock and run `treeman worktree finalize`", lock, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
