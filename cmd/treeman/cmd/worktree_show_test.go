package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/ui"
)

// TestTearingDownPaths asserts the picker-side filter: a worktree
// mid-teardown surfaces its path (so occupiedWorktrees drops it from
// the switch/delete pickers), and drops off the set once teardown
// ends.
func TestTearingDownPaths(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "treeman.db")
	t.Setenv("TREEMAN_DB_PATH", dbPath)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtID, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/wt", "feature-x", "feature-x")

	tearing := func() map[string]bool {
		t.Helper()
		paths, err := tearingDownPaths(ctx, "/tmp/repo")
		if err != nil {
			t.Fatal(err)
		}
		return paths
	}
	if paths := tearing(); len(paths) != 0 {
		t.Fatalf("no delete events yet — want empty set, got %v", paths)
	}
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeDeleteStart, "start", repoID, wtID, "", 0, nil)
	if paths := tearing(); !paths["/tmp/repo/wt"] {
		t.Fatalf("teardown in flight — want /tmp/repo/wt in set, got %v", paths)
	}
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeDeleteEnd, "end", repoID, wtID, "", 0, nil)
	if paths := tearing(); len(paths) != 0 {
		t.Fatalf("teardown done — want empty set, got %v", paths)
	}
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeDeleteStart, "start2", repoID, wtID, "", 0, nil)
	_ = st.WriteEvent(ctx, store.LevelError, store.EvtWorktreeDeleteError, "boom", repoID, wtID, "", 0, nil)
	if paths := tearing(); len(paths) != 0 {
		t.Fatalf("delete:error is latest — a failed teardown must reappear, got %v", paths)
	}
}

// TestPrintWtPrepareSummary seeds prepare:end + prepare:phase events
// and asserts the "last prepare" block renders the newest run per
// database — outcome, duration, clone count, and the per-phase
// breakdown — without leaking older runs.
func TestPrintWtPrepareSummary(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtID, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/wt", "feature-x", "feature-x")

	// Older run (cold build) then newer run (cache hit) for the same DB
	// — only the newer must render.
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtPrepareEnd, "old",
		repoID, wtID, "", 0, map[string]string{
			"engine": "postgres", "source_db": "app_x",
			"cache_hit": "false", "duration_ms": "9000", "clones": "2",
		})
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtPreparePhase, "phase",
		repoID, wtID, "migrate", 1200, map[string]string{
			"engine": "postgres", "source_db": "app_x", "duration_ms": "1200",
		})
	_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtPrepareEnd, "new",
		repoID, wtID, "", 0, map[string]string{
			"engine": "postgres", "source_db": "app_x",
			"cache_hit": "true", "duration_ms": "412", "clones": "2",
		})

	var buf bytes.Buffer
	oldOut := ui.Out
	ui.Out = &buf
	defer func() { ui.Out = oldOut }()

	if err := printWtPrepareSummary(ctx, st, wtID); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"last prepare", "postgres", "app_x", "cache hit", "412ms", "2 clones", "migrate 1.2s"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "9s") {
		t.Errorf("older run leaked into summary:\n%s", out)
	}
}
