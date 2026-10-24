package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stubbedev/treeman/internal/store"
)

func storeWithRepo(t *testing.T, repo string) string {
	t.Helper()
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "t.db")
	t.Setenv("TREEMAN_DB_PATH", dbFile)
	s, err := store.Open(ctx, dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	repoID, err := s.EnsureRepo(ctx, repo, filepath.Base(repo))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureWorktree(ctx, repoID, filepath.Join(repo, ".wt", "x"), "x", "feat/x"); err != nil {
		t.Fatal(err)
	}
	return dbFile
}

func dropTables(t *testing.T, dbFile string, tables ...string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, tbl := range tables {
		if _, err := s.DB.ExecContext(ctx, "DROP TABLE "+tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
}

func TestCompletionFailsWhenTheRegistryCannotBeRead(t *testing.T) {
	dbFile := storeWithRepo(t, "/r/foo")
	dropTables(t, dbFile, "worktree_ports", "hook_runs", "worktrees")
	_, err := completionHandler(context.Background(), &mcpsdk.CompleteRequest{Params: &mcpsdk.CompleteParams{
		Ref:      &mcpsdk.CompleteReference{Type: "ref/prompt", Name: "p"},
		Argument: mcpsdk.CompleteParamsArgument{Name: "worktree"},
	}})
	if err == nil || !strings.Contains(err.Error(), "worktree slugs") {
		t.Fatalf("completion over an unreadable registry = %v, want an error naming what was listed", err)
	}
}

func TestConfigDeleteKeepsTheFileWhenTheSnapshotFails(t *testing.T) {
	repo := newTempRepo(t)
	dbFile := storeWithRepo(t, repo)
	dropTables(t, dbFile, "config_generations")
	cfg := filepath.Join(repo, ".treeman.yaml")
	if err := os.WriteFile(cfg, []byte("databases: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, err := configDeleteTool(context.Background(), nil, configDeleteIn{Repo: repo, Ack: true})
	if err == nil {
		t.Fatalf("config_delete with a failing history snapshot succeeded: %+v", out)
	}
	if _, statErr := os.Stat(cfg); statErr != nil {
		t.Fatalf("config_delete removed %s although it could not snapshot it first: %v", cfg, statErr)
	}
}

func TestWorktreeShowFailsWhenItsPortsCannotBeRead(t *testing.T) {
	repo := newTempRepo(t)
	dbFile := storeWithRepo(t, repo)
	dropTables(t, dbFile, "worktree_ports")
	_, out, err := worktreeShowTool(context.Background(), nil, worktreeShowIn{Repo: repo, Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "ports") {
		t.Fatalf("worktree_show over an unreadable ports table = %+v / %v, want an error naming the ports", out, err)
	}
}

func TestLogsQueryRejectsAnUnenrolledRepoInsteadOfWideningTheQuery(t *testing.T) {
	repo := newTempRepo(t)
	storeWithRepo(t, "/r/other")
	_, out, err := logsQueryTool(context.Background(), nil, logsQueryIn{Repo: repo})
	if err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("logs_query for an unenrolled repo = %d events / %v, want a not-enrolled error", len(out.Events), err)
	}
}

func TestLogsSubscribeFailsWhenTheEventLogCannotBeRead(t *testing.T) {
	dbFile := storeWithRepo(t, "/r/foo")
	dropTables(t, dbFile, "events")
	_, out, err := logsSubscribeTool(context.Background(), nil, logsSubscribeIn{TimeoutSeconds: 1})
	if err == nil {
		t.Fatalf("logs_subscribe over an unreadable event log returned %+v and no error", out)
	}
}

func TestMainWorktreeStatusFailsWhenTheRegistryCannotBeRead(t *testing.T) {
	repo := newTempRepo(t)
	dbFile := storeWithRepo(t, repo)
	dropTables(t, dbFile, "worktree_ports", "hook_runs", "worktrees")
	_, out, err := mainWorktreeStatus(context.Background(), repo)
	if err == nil {
		t.Fatalf("main_worktree status over an unreadable registry = %+v, want an error", out)
	}
}

func TestWorktreeResourceDistinguishesALookupFailureFromAMissingWorktree(t *testing.T) {
	dbFile := storeWithRepo(t, "/r/foo")
	dropTables(t, dbFile, "worktree_ports", "hook_runs", "worktrees")
	uri := "treeman://worktrees/x/events"
	_, err := worktreeEventsResource(context.Background(), &mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: uri}})
	if err == nil || err.Error() == mcpsdk.ResourceNotFoundError(uri).Error() || !strings.Contains(err.Error(), "look up worktree") {
		t.Fatalf("resource read over an unreadable registry = %v, want a lookup error, not not-found", err)
	}
}
