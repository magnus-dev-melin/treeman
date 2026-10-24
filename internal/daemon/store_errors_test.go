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
