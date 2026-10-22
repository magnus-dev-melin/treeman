package prepare

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/snapshot"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/template"
)

func TestRollbackFallbackReportsTheFailingStepsOutput(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repoID, err := st.EnsureRepo(ctx, "/r", "r")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, "/r/wt", "wt", "main")
	if err != nil {
		t.Fatal(err)
	}

	d := config.DatabaseConfig{
		Engine:   "mysql",
		Migrate:  &config.Step{Run: "echo 'Column not found: dead_column' >&2; exit 1"},
		Rollback: &config.Step{Run: "true"},
	}
	key := snapshot.New("mysql", "8.0", "", "", "", "", map[string]string{store.CommandsHashKey: commandsHash(d)})
	const glob = "migrations/*.php"
	if err := st.RecordSnapshot(ctx, store.SnapshotRecord{
		Fingerprint:    "ancestor",
		Engine:         "mysql",
		EngineVersion:  "8.0",
		SourceDB:       "src",
		TemplateName:   "_tm_ancestor",
		LockfileHashes: map[string]string{store.CommandsHashKey: commandsHash(d)},
		Inputs: map[string]store.InputVector{glob: {
			{Path: "migrations/001.php", Hash: "h1"},
			{Path: "migrations/002.php", Hash: "hOLD"},
		}},
		RepoID: repoID,
	}); err != nil {
		t.Fatal(err)
	}
	current := map[string]store.InputVector{glob: {
		{Path: "migrations/001.php", Hash: "h1"},
		{Path: "migrations/002.php", Hash: "hNEW"},
	}}
	ops := incrementalOps{
		exists:          func(context.Context, string) (bool, error) { return true, nil },
		snapshotRestore: func(context.Context, string, string) error { return nil },
	}

	_, done, err := tryRollbackIncrementalBuild(ctx, &config.Config{}, d, template.Context{}, t.TempDir(), st,
		repoID, wtID, "src", key.TemplateName(), "8.0", 0, key, current, nil, time.Now(), ops)
	if err != nil || done {
		t.Fatalf("a failed re-migrate must fall through to the next build path: done=%v err=%v", done, err)
	}

	evs, err := st.QueryEvents(ctx, store.EventFilter{WorktreeID: wtID, EventTypes: []string{store.EvtPrepareRollbackFallback}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("rollback fallback events = %d, want 1", len(evs))
	}
	msg := evs[0].Message
	if !strings.Contains(msg, "Column not found: dead_column") || strings.Contains(msg, "<nil>") {
		t.Errorf("fallback reason must carry the migrate stderr, got %q", msg)
	}

	errEvs, err := st.QueryEvents(ctx, store.EventFilter{WorktreeID: wtID, EventTypes: []string{store.EvtPrepareError}})
	if err != nil {
		t.Fatal(err)
	}
	if len(errEvs) != 0 {
		t.Errorf("a recovered rollback step must not emit a terminal prepare:error, got %+v", errEvs)
	}
}
