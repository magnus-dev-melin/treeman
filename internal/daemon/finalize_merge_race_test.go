package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/store"
)

func TestFinalizeFailedDuringMergeReRunsWhenMergeClears(t *testing.T) {
	requireGitAutofetch(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TREEMAN_CONFIG", "")
	st := newTestState(t)
	ctx := t.Context()

	repo := filepath.Join(t.TempDir(), "repo")
	gitRun(t, "", "init", "-q", "-b", "main", repo)
	gitRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	cfg := "auto_fetch:\n  enabled: false\nhooks:\n  create-before-engines:\n    - run: test -f merged\n"
	if err := os.WriteFile(filepath.Join(repo, ".treeman.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".worktrees", "feat")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feat", wt)

	if err := FinalizeWorktree(ctx, st, repo, wt, nil, false); err == nil {
		t.Fatal("hook against the unmerged tree must fail")
	}
	if !st.HasWtWatcher(wt) {
		t.Fatal("a finalize whose setup hook failed left no HEAD watcher to observe the merge")
	}

	gitDir, err := resolveGitDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	mergeHead := filepath.Join(gitDir, "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte("0000000000000000000000000000000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(wt, "merged"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mergeHead); err != nil {
		t.Fatal(err)
	}

	wtRow, err := st.Store.LookupActiveWorktreeByPath(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := st.Store.QueryEvents(ctx, store.EventFilter{
			WorktreeID: wtRow.ID,
			EventTypes: []string{store.EvtWorktreeCreateEnd, store.EvtWorktreeCreateError},
			Limit:      1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) == 1 && evs[0].EventType == store.EvtWorktreeCreateEnd {
			if err := st.WaitFinalizeCleared(context.Background(), wt, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("clearing the merge did not re-run finalize to a ready state")
}
