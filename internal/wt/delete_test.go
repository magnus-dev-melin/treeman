package wt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func TestDeleteValidation(t *testing.T) {
	ctx := context.Background()
	_, err := Delete(ctx, DeleteRequest{}, NoopSink{})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("missing target should error, got %v", err)
	}
	_, err = Delete(ctx, DeleteRequest{Target: "x"}, NoopSink{})
	if err == nil || !strings.Contains(err.Error(), "repo_root is required") {
		t.Fatalf("missing repo_root should error, got %v", err)
	}
}

func TestDeleteNoMatchWithoutForce(t *testing.T) {
	withTempStore(t)
	// Repo root that exists; target that doesn't match anything.
	repoRoot := t.TempDir()
	missingPath := filepath.Join(repoRoot, "does-not-exist")
	_, err := Delete(context.Background(), DeleteRequest{
		RepoRoot: repoRoot,
		Target:   missingPath,
	}, NoopSink{})
	if err == nil {
		t.Fatal("expected error for missing target without --force")
	}
	if !strings.Contains(err.Error(), "no worktree matches") {
		t.Errorf("error should mention 'no worktree matches', got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should hint at --force, got: %v", err)
	}
}

// TestDeleteRefusesMainWorktree guards against dropping the repo's
// primary checkout (and, for branch_scoped configs, the bare app DB):
// deleting a path equal to the repo root must be refused before any
// teardown runs — even with --force.
func TestDeleteRefusesMainWorktree(t *testing.T) {
	withTempStore(t)
	repoRoot := t.TempDir()
	_, err := Delete(context.Background(), DeleteRequest{
		RepoRoot: repoRoot,
		Target:   repoRoot,
		Force:    true, // force must NOT bypass the main-worktree guard
	}, NoopSink{})
	if err == nil || !strings.Contains(err.Error(), "main worktree") {
		t.Fatalf("deleting the repo root must be refused, got %v", err)
	}
}

// TestDeleteRefusesTeardownInFlight — the double-delete guard: a
// second delete against a worktree whose teardown is already running
// (delete:start with no end/error) must be refused; Force stays as
// the escape hatch for a hung teardown.
func TestDeleteRefusesTeardownInFlight(t *testing.T) {
	ctx := context.Background()
	st := withTempStore(t)
	repoRoot := t.TempDir()
	wtPath := filepath.Join(repoRoot, ".worktrees", "feature-x")
	repoID, err := st.EnsureRepo(ctx, repoRoot, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, wtPath, "app_feature-x", "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeDeleteStart, "start", repoID, wtID, "", 0, nil)

	// By slug: LookupWorktree skips tearing-down rows, so the target
	// resolves to nothing — no second teardown fires.
	_, err = Delete(ctx, DeleteRequest{RepoRoot: repoRoot, Target: "app_feature-x"}, NoopSink{})
	if err == nil || !strings.Contains(err.Error(), "no worktree matches") {
		t.Fatalf("slug of tearing-down worktree must not resolve, got %v", err)
	}

	// By explicit path: bypasses the lookup, so the path guard must
	// catch it.
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Delete(ctx, DeleteRequest{RepoRoot: repoRoot, Target: wtPath}, NoopSink{})
	if err == nil || !strings.Contains(err.Error(), "teardown already in progress") {
		t.Fatalf("path of tearing-down worktree must be refused, got %v", err)
	}
}

// A dirty worktree whose delete was already confirmed used to lose its
// databases and registry row, then have git refuse the removal — the
// worktree stayed on disk, unregistered, and every retry failed the
// same way. The inline path must finish the removal.
func TestInlineDeleteOfDirtyWorktreeRemovesIt(t *testing.T) {
	ctx := context.Background()
	st := withTempStore(t)
	repo := t.TempDir()
	mkRepoOnBranch(t, repo, "main")
	wtPath := filepath.Join(repo, ".worktrees", "feature-x")
	addLinkedWorktree(t, repo, wtPath, "feature/x")
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Delete(ctx, DeleteRequest{RepoRoot: repo, Target: wtPath, Detached: true}, NoopSink{})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Status != DeleteInline {
		t.Fatalf("status = %v, want inline", res.Status)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}
	repoID, _ := st.LookupRepoID(ctx, repo)
	var deleted bool
	if err := st.DB.QueryRowContext(ctx,
		`SELECT deleted_at IS NOT NULL FROM worktrees WHERE repo_id = ? AND path = ?`, repoID, wtPath).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Error("registry row not marked deleted")
	}
}
