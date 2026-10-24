package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/pkg/rpc"
)

func TestSubscribeToAnUnregisteredRepoIsRefused(t *testing.T) {
	st := newTestState(t)
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	go streamEvents(t.Context(), st, server, rpc.Request{
		Method:         rpc.MethodEventSubscribe,
		EventSubscribe: &rpc.EventSubscribeArgs{RepoPath: "/nowhere/registered"},
	})
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp rpc.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Kind != rpc.KindError || !strings.Contains(resp.Message, "/nowhere/registered") {
		t.Fatalf("subscribing to an unregistered repo = %+v; want a refusal naming it, not an unscoped stream", resp)
	}
}

func TestConfigWriteRefusesWhenTheHistorySnapshotFails(t *testing.T) {
	st := newTestState(t)
	path := filepath.Join(t.TempDir(), ".treeman.yaml")
	if err := os.WriteFile(path, []byte("old: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Store.DB.ExecContext(t.Context(), "DROP TABLE config_generations"); err != nil {
		t.Fatal(err)
	}
	_, err := runTaskConfigWrite(t.Context(), st, rpc.Task{
		RepoPath: filepath.Dir(path),
		Params:   map[string]string{rpc.ParamPath: path, rpc.ParamBody: "new: 2\n"},
	})
	if err == nil {
		t.Fatal("a config write whose history snapshot failed must fail")
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != "old: 1\n" {
		t.Fatalf("the config file must be left untouched, got %q, %v", got, rerr)
	}
}

func TestSyncStatusSurfacesStoreErrors(t *testing.T) {
	st := newTestState(t)
	if _, err := st.Store.DB.ExecContext(t.Context(), "DROP TABLE repos"); err != nil {
		t.Fatal(err)
	}
	if resp := handleSyncStatus(t.Context(), st, rpc.Request{Method: rpc.MethodSyncStatus}); resp.Kind != rpc.KindError {
		t.Fatalf("sync status over an unreadable registry = %+v; want an error, not an empty status list", resp)
	}
}

func TestDaemonRunsSkipWithAnErrorWhenTheCapturedEnvIsUnreadable(t *testing.T) {
	st := newTestState(t)
	ctx := context.Background()
	repoID, _ := st.Store.EnsureRepo(ctx, "/r", "r")
	if _, err := st.Store.EnsureWorktree(ctx, repoID, "/r/.wt/a", "a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Store.DB.ExecContext(ctx, `UPDATE worktrees SET inherited_env_json = '{broken' WHERE path = '/r/.wt/a'`); err != nil {
		t.Fatal(err)
	}
	if _, ok := inheritedEnvOrSkip(ctx, st, repoID, "/r/.wt/a", "watch re-prepare"); ok {
		t.Fatal("an unreadable captured env must skip the run, not run it with no PATH")
	}
	evs, err := st.Store.QueryEvents(ctx, store.EventFilter{RepoID: repoID, EventTypes: []string{store.EvtWatchSkip}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Level != store.LevelError || !strings.Contains(evs[0].Message, "/r/.wt/a") {
		t.Fatalf("the skip must be recorded as an error naming the worktree, got %+v", evs)
	}
}

func TestTeardownFailureRecordsDeleteError(t *testing.T) {
	requireGitAutofetch(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TREEMAN_CONFIG", "")
	st, repo, wt := newWatchWorktreeWithConfig(t, "auto_fetch:\n  enabled: false\n")
	if err := FinalizeWorktree(t.Context(), st, repo, wt, nil, false); err != nil {
		t.Fatal(err)
	}
	row, err := st.Store.LookupActiveWorktreeByPath(t.Context(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Store.DB.ExecContext(t.Context(),
		`CREATE TRIGGER block_delete BEFORE UPDATE OF deleted_at ON worktrees BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := TeardownWorktree(t.Context(), st, repo, wt, false, nil); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("a teardown that cannot mark the row deleted must fail with the cause, got %v", err)
	}
	evs, err := st.Store.QueryEvents(t.Context(), store.EventFilter{
		WorktreeID: row.ID,
		EventTypes: []string{store.EvtWorktreeDeleteStart, store.EvtWorktreeDeleteEnd, store.EvtWorktreeDeleteError},
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].EventType != store.EvtWorktreeDeleteError || evs[0].Level != store.LevelError {
		t.Fatalf("a failed teardown must end in delete:error so the worktree stops reading as tearing down, got %+v", evs)
	}
	last, err := st.Store.LatestWorktreeStateEvent(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := store.DeriveWorktreeState(last); state != store.StateError {
		t.Fatalf("a failed teardown must read as error, not %q", state)
	}
}

func TestReconcileStampsCLIRowsAndReapsTheirVanishedWorktrees(t *testing.T) {
	requireGitAutofetch(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TREEMAN_CONFIG", "")
	st := newTestState(t)
	ctx := t.Context()
	repo := filepath.Join(t.TempDir(), "repo")
	gitRun(t, "", "init", "-q", "-b", "main", repo)
	gitRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	live := filepath.Join(repo, ".worktrees", "live")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "live", live)
	gone := filepath.Join(repo, ".worktrees", "gone")

	repoID, err := st.Store.EnsureRepo(ctx, repo, "repo")
	if err != nil {
		t.Fatal(err)
	}
	liveID, err := st.Store.EnsureWorktree(ctx, repoID, live, "live", "live")
	if err != nil {
		t.Fatal(err)
	}
	goneID, err := st.Store.EnsureWorktree(ctx, repoID, gone, "gone", "gone")
	if err != nil {
		t.Fatal(err)
	}

	lw := &LifecycleWatcher{
		repoID: repoID, repoPath: repo, adminRoot: filepath.Join(repo, ".git", "worktrees"), st: st,
		seen: map[string]string{}, pendingCreate: map[string]*time.Timer{},
	}
	if err := lw.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	admin := filepath.Join(repo, ".git", "worktrees", "live")
	byAdmin, err := st.Store.LookupWorktreeByAdminDir(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if byAdmin.ID != liveID {
		t.Fatalf("reconcile must record admin dir %s for the CLI-registered worktree #%d, got row %+v", admin, liveID, byAdmin)
	}
	rows, err := st.Store.ListWorktreesForRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == goneID && !r.Deleted {
			t.Fatalf("a CLI-registered worktree removed while the daemon was down must be reaped, got %+v", r)
		}
		if r.ID == liveID && r.Deleted {
			t.Fatalf("the live worktree was reaped: %+v", r)
		}
	}
}
