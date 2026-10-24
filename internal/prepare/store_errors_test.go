package prepare

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
)

func failWrites(t *testing.T, st *store.Store, name, sql string) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(),
		"CREATE TRIGGER "+name+" "+sql+" BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
}

func TestBranchScopedFailsInsteadOfTreatingAnUnreadableBranchAsDetached(t *testing.T) {
	f := newBSFixture(t)
	ctx := context.Background()
	if _, err := f.st.DB.ExecContext(ctx, "ALTER TABLE worktrees RENAME TO worktrees_unreadable"); err != nil {
		t.Fatal(err)
	}
	_, err := runBranchScoped(ctx, f.args(nil, nil, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "read the branch of worktree") {
		t.Fatalf("runBranchScoped = %v; want the branch read error", err)
	}
	if _, ok := f.fake.data[f.active]; ok {
		t.Fatal("the active namespace was seeded for a branch that could not be read")
	}
}

func TestBranchScopedFailsWhenTheCleanMarkerCannotBeRecorded(t *testing.T) {
	f := newBSFixture(t)
	f.set(f.active, map[string]string{"d": "1"})
	failWrites(t, f.st, "no_clean", "BEFORE UPDATE OF clean ON active_branch_db WHEN NEW.clean = 1")
	if _, err := f.runErr("develop"); err == nil || !strings.Contains(err.Error(), "clean marker") {
		t.Fatalf("adopt with an unwritable clean marker = %v; want the clean-marker error", err)
	}
}

func TestBranchScopedFailsWhenTheCapturedDurableCannotBeTracked(t *testing.T) {
	f := newBSFixture(t)
	f.set(f.active, map[string]string{"d": "1"})
	f.run("develop")
	f.write(f.active, "x", "2")
	failWrites(t, f.st, "no_durables", "BEFORE INSERT ON branch_durables")
	failWrites(t, f.st, "no_durable_updates", "BEFORE UPDATE ON branch_durables")
	if _, err := f.runErr("feature"); err == nil || !strings.Contains(err.Error(), "record durable") {
		t.Fatalf("swap with an untrackable durable = %v; want the durable-record error", err)
	}
}

func TestTeardownKeepsTheActiveNamespaceWhenItsCaptureFails(t *testing.T) {
	f := newBSFixture(t)
	f.set(f.active, map[string]string{"d": "1"})
	f.run("develop")
	f.write(f.active, "x", "2")
	failWrites(t, f.st, "no_durables", "BEFORE INSERT ON branch_durables")
	failWrites(t, f.st, "no_durable_updates", "BEFORE UPDATE ON branch_durables")

	err := teardownActive(context.Background(), f.eng, f.st, f.repoID, f.worktreeID, f.active)
	if err == nil {
		t.Fatal("teardown dropped the active namespace although its capture failed")
	}
	if got := f.fake.data[f.active]; got["x"] != "2" {
		t.Fatalf("active namespace after a failed teardown capture = %v; want it kept", got)
	}
	f.assertMarker("develop")
	if f.eventCount(store.EvtBranchCaptureError) != 1 {
		t.Fatalf("want one branch:capture:error event, got %d", f.eventCount(store.EvtBranchCaptureError))
	}
}

func TestSiblingSlugsFailsInsteadOfReportingNoSiblings(t *testing.T) {
	f := newBSFixture(t)
	if _, err := f.st.DB.ExecContext(context.Background(), "ALTER TABLE worktrees RENAME TO worktrees_unreadable"); err != nil {
		t.Fatal(err)
	}
	if others, err := siblingSlugs(context.Background(), f.st, f.repoID, f.worktreeID); err == nil {
		t.Fatalf("siblingSlugs = %v, nil; an unreadable registry must not read as no siblings (a filtered drop would wipe them)", others)
	}
	f.eng.scope = scopePrefix
	a := f.args(nil, nil, nil, nil)
	if keep, err := a.parentSourceKeep(context.Background(), "app_"); err == nil {
		t.Fatalf("parentSourceKeep = %v, nil; an unreadable registry must not widen the parent seed to every sibling", keep != nil)
	}
}

func TestRunFailsWhenTheBuiltAtFingerprintCannotBeRecorded(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wt := t.TempDir()
	repoID, err := st.EnsureRepo(ctx, wt, "repo")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, wt, "wtslug", "develop")
	if err != nil {
		t.Fatal(err)
	}
	failWrites(t, st, "no_built", "BEFORE INSERT ON template_db_built")
	cfg := &config.Config{Databases: []config.DatabaseConfig{
		{Engine: "sqlite", NameTemplate: "data/app_{slug}.db", Migrate: &config.Step{Run: "mkdir -p data && touch \"$TREEMAN_TARGET_DB\""}},
	}}
	_, err = Run(ctx, cfg, wt, slug.Slug{Value: "wtslug", Source: slug.SourceTicket}, st, repoID, wtID, nil)
	if err == nil || !strings.Contains(err.Error(), "was built at") {
		t.Fatalf("Run with an unwritable built-at marker = %v; want the built-at error (a stale marker would skip a needed restore)", err)
	}
}

func TestSnapshotKeyFailsOnAnInputGlobItCannotExpand(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := config.DatabaseConfig{Engine: "postgres", Inputs: []config.Input{{Glob: "migrations/[unclosed"}}}
	if k, err := computeSnapshotKey(ctx, st, d, t.TempDir(), "16"); err == nil {
		t.Fatalf("computeSnapshotKey = %s, nil; an unexpandable input glob must not drop out of the fingerprint", k.Fingerprint())
	}
}
