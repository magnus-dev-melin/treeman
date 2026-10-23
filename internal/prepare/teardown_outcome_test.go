package prepare

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/store"
)

func teardownOutcomeStore(t *testing.T) (*store.Store, int64, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repoID, err := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, "/tmp/repo/.worktrees/x", "x", "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveBranch(ctx, repoID, wtID, "app_x", "feature/x", "mysql"); err != nil {
		t.Fatal(err)
	}
	return st, repoID, wtID
}

// A failed drop must stay retryable: the error is returned and kept on
// the worktree row, and the active-branch marker survives so the retry
// can still capture the branch before dropping (#171).
func TestTeardownDatabasesFailureIsRecordedForRetry(t *testing.T) {
	ctx := context.Background()
	st, repoID, wtID := teardownOutcomeStore(t)
	cfg := &config.Config{Databases: []config.DatabaseConfig{{Engine: "sqlite", NameTemplate: ""}}}

	err := TeardownDatabases(ctx, cfg, "x", repoID, wtID, st)
	if err == nil || !strings.Contains(err.Error(), "renders empty") {
		t.Fatalf("TeardownDatabases err = %v, want the render failure", err)
	}
	if err := st.MarkWorktreeDeleted(ctx, wtID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingTeardowns(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].WorktreeID != wtID || !strings.Contains(pending[0].Error, "renders empty") {
		t.Fatalf("pending = %+v, want worktree %d with the render failure", pending, wtID)
	}
	if branch, ok, err := st.GetActiveBranch(ctx, wtID, "app_x"); err != nil || !ok || branch != "feature/x" {
		t.Fatalf("active-branch marker = %q ok=%v err=%v, want it kept", branch, ok, err)
	}
}

func TestTeardownDatabasesSuccessClearsPendingAndMarkers(t *testing.T) {
	ctx := context.Background()
	st, repoID, wtID := teardownOutcomeStore(t)
	if err := st.SetTeardownError(ctx, wtID, "Error 1040: Too many connections"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWorktreeDeleted(ctx, wtID); err != nil {
		t.Fatal(err)
	}

	if err := TeardownDatabases(ctx, &config.Config{}, "x", repoID, wtID, st); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}
	pending, err := st.ListPendingTeardowns(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none after a successful teardown", pending)
	}
	if _, ok, err := st.GetActiveBranch(ctx, wtID, "app_x"); err != nil || ok {
		t.Fatalf("active-branch marker ok=%v err=%v, want it cleared", ok, err)
	}
}
