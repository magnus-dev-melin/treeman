package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func watchLifecycleEvents(t *testing.T, st *State, wt string) []store.Event {
	t.Helper()
	row, err := st.Store.LookupActiveWorktreeByPath(t.Context(), wt)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Store.QueryEvents(t.Context(), store.EventFilter{
		WorktreeID: row.ID,
		EventTypes: []string{store.EvtWorktreeCreateStart, store.EvtWorktreeCreateEnd, store.EvtWorktreeCreateError},
	})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func newWatchWorktree(t *testing.T, migrate string) (st *State, repo, wt string) {
	t.Helper()
	requireGitAutofetch(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TREEMAN_CONFIG", "")
	st = newTestState(t)
	repo = filepath.Join(t.TempDir(), "repo")
	gitRun(t, "", "init", "-q", "-b", "main", repo)
	gitRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	cfg := "auto_fetch:\n  enabled: false\ndatabases:\n  - engine: sqlite\n    name_template: data/{slug}.db\n    migrate:\n      run: " + migrate + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".treeman.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	wt = filepath.Join(repo, ".worktrees", "feat")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	return st, repo, wt
}

func TestWatchRePrepareMarksWorktreePreparingThenReady(t *testing.T) {
	st, repo, wt := newWatchWorktree(t, "'mkdir -p data && touch \"$TREEMAN_TARGET_DB\"'")
	if err := FinalizeWorktreeForWatch(t.Context(), st, repo, wt, 0, nil); err != nil {
		t.Fatal(err)
	}
	evs := watchLifecycleEvents(t, st, wt)
	if len(evs) != 2 || evs[1].EventType != store.EvtWorktreeCreateStart || evs[0].EventType != store.EvtWorktreeCreateEnd {
		t.Fatalf("watch re-prepare must be bracketed by create:start and create:end, got %+v", evs)
	}
	if !strings.Contains(evs[1].PayloadJSON, string(triggerFileChange)) {
		t.Errorf("create:start must name the file-change trigger, got %s", evs[1].PayloadJSON)
	}
}

func TestWatchRePrepareFailureLeavesWorktreeInError(t *testing.T) {
	st, repo, wt := newWatchWorktree(t, "'exit 1'")
	if err := FinalizeWorktreeForWatch(t.Context(), st, repo, wt, 0, nil); err == nil {
		t.Fatal("a failed migrate must fail the watch re-prepare")
	}
	evs := watchLifecycleEvents(t, st, wt)
	if len(evs) == 0 || evs[0].EventType != store.EvtWorktreeCreateError || evs[0].Level != store.LevelError {
		t.Fatalf("a failed watch re-prepare must leave the worktree in error, not ready, got %+v", evs)
	}
}
