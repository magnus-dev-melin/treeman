package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestListPendingTeardowns(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/r", "r")
	if err != nil {
		t.Fatal(err)
	}
	add := func(path, slug string, deleted bool) int64 {
		t.Helper()
		id, err := st.EnsureWorktree(ctx, repoID, path, slug, "b")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetTeardownError(ctx, id, "boom "+slug); err != nil {
			t.Fatal(err)
		}
		if deleted {
			if err := st.MarkWorktreeDeleted(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	pendingID := add("/r/.worktrees/a", "a", true)
	add("/r/.worktrees/live", "live", false)
	add("/r/.worktrees/old-b", "b", true)
	add("/r/.worktrees/new-b", "b", false)

	got, err := st.ListPendingTeardowns(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WorktreeID != pendingID || got[0].Slug != "a" || got[0].Error != "boom a" {
		t.Fatalf("pending = %+v, want only the deleted worktree whose slug no live worktree owns", got)
	}

	if err := st.ClearTeardownError(ctx, pendingID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListPendingTeardowns(ctx, repoID); err != nil || len(got) != 0 {
		t.Fatalf("after clear: pending = %+v err=%v, want none", got, err)
	}
}

func TestRecreatingWorktreeDropsPendingTeardown(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/r", "r")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.EnsureWorktree(ctx, repoID, "/r/.worktrees/a", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTeardownError(ctx, id, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWorktreeDeleted(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureWorktree(ctx, repoID, "/r/.worktrees/a", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWorktreeDeleted(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListPendingTeardowns(ctx, repoID); err != nil || len(got) != 0 {
		t.Fatalf("pending = %+v err=%v, want the re-created worktree's old error gone", got, err)
	}
}
