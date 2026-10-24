package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func openStatusStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func bucketOf(t *testing.T, s *store.Store, path string) string {
	t.Helper()
	out, err := statusOverview(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out.Worktrees {
		if w.Path == path {
			return w.Bucket
		}
	}
	t.Fatalf("worktree %s missing from the status overview %+v", path, out)
	return ""
}

func TestStatusOverviewPrepareClearsError(t *testing.T) {
	ctx := context.Background()
	s := openStatusStore(t)
	repoID, _ := s.EnsureRepo(ctx, "/r", "r")
	wtID, _ := s.EnsureWorktree(ctx, repoID, "/r/.wt/feat", "feat", "feat")

	write := func(level, evt string) {
		if err := s.WriteEvent(ctx, level, evt, "m", repoID, wtID, "", 0, nil); err != nil {
			t.Fatal(err)
		}
	}

	write(store.LevelError, store.EvtWorktreeCreateError)
	if b := bucketOf(t, s, "/r/.wt/feat"); b != store.BucketFailed {
		t.Fatalf("after create:error bucket=%q, want failed", b)
	}
	write(store.LevelInfo, store.EvtPrepareEnd)
	if b := bucketOf(t, s, "/r/.wt/feat"); b != store.BucketStable {
		t.Fatalf("after recovery prepare:end bucket=%q, want stable", b)
	}
	write(store.LevelError, store.EvtPrepareError)
	if b := bucketOf(t, s, "/r/.wt/feat"); b != store.BucketFailed {
		t.Fatalf("after prepare:error bucket=%q, want failed", b)
	}
}

func TestStatusOverviewDerivesEveryWorktreeFromItsOwnLatestEvent(t *testing.T) {
	ctx := context.Background()
	s := openStatusStore(t)
	repoID, _ := s.EnsureRepo(ctx, "/r", "r")
	events := map[string][]string{
		"/r/.wt/a": {store.EvtWorktreeCreateError, store.EvtWorktreeCreateEnd},
		"/r/.wt/b": {store.EvtWorktreeCreateEnd, store.EvtWorktreeCreateStart},
		"/r/.wt/c": {store.EvtWorktreeCreateEnd, store.EvtWorktreeCreateError},
		"/r/.wt/d": nil,
	}
	for _, path := range []string{"/r/.wt/a", "/r/.wt/b", "/r/.wt/c", "/r/.wt/d"} {
		wtID, err := s.EnsureWorktree(ctx, repoID, path, filepath.Base(path), filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		for _, evt := range events[path] {
			level := store.LevelInfo
			if evt == store.EvtWorktreeCreateError {
				level = store.LevelError
			}
			if err := s.WriteEvent(ctx, level, evt, "m", repoID, wtID, "", 0, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := statusOverview(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/r/.wt/a": store.BucketStable,
		"/r/.wt/b": store.BucketUp,
		"/r/.wt/c": store.BucketFailed,
		"/r/.wt/d": store.BucketStable,
	}
	if out.Total != 4 || out.Stable != 2 || out.Up != 1 || out.Failed != 1 {
		t.Errorf("counts = %+v, want total 4, stable 2, up 1, failed 1", out)
	}
	for _, w := range out.Worktrees {
		if w.Bucket != want[w.Path] {
			t.Errorf("%s bucket = %q, want %q", w.Path, w.Bucket, want[w.Path])
		}
	}
}

func TestStatusOverviewSurfacesLifecycleQueryErrors(t *testing.T) {
	ctx := context.Background()
	s := openStatusStore(t)
	repoID, _ := s.EnsureRepo(ctx, "/r", "r")
	if _, err := s.EnsureWorktree(ctx, repoID, "/r/.wt/feat", "feat", "feat"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "DROP TABLE events"); err != nil {
		t.Fatal(err)
	}
	if _, err := statusOverview(ctx, s); err == nil {
		t.Fatal("a failed lifecycle-state query must fail the status overview, not report every worktree stable")
	}
}
