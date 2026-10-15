package prepare

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
)

// TestRunFreshRestoresClonesAtCurrentFingerprint: a cache-hit prepare
// leaves test clones alone when they are already at the current
// fingerprint, so state a test run wrote into them survives; a fresh
// prepare restores them from the template regardless.
func TestRunFreshRestoresClonesAtCurrentFingerprint(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TREEMAN_DB_PATH", filepath.Join(t.TempDir(), "treeman.db"))
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
	if err := os.MkdirAll(filepath.Join(wt, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(wt, "data", "app_wtslug.db")
	clone := filepath.Join(wt, "data", "app_wtslug_test_1.db")
	cfg := &config.Config{Databases: []config.DatabaseConfig{{
		Engine:       "sqlite",
		NameTemplate: "data/app_{slug}.db",
		Migrate:      &config.Step{Run: "touch " + source},
		TestClones:   &config.TestClonesSpec{Clones: config.ClonesSetting{Fixed: 1}, NameTemplate: "data/app_{slug}_test_{n}.db"},
	}}}
	sl := slug.Slug{Value: "wtslug", Source: slug.SourceTicket}
	prepareWith := func(opts RunOptions) {
		t.Helper()
		if _, err := RunFiltered(ctx, cfg, wt, sl, st, repoID, wtID, nil, opts); err != nil {
			t.Fatal(err)
		}
	}
	pollute := func() {
		t.Helper()
		if err := os.WriteFile(clone, []byte("left behind by a test run"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cloneSize := func() int64 {
		t.Helper()
		info, err := os.Stat(clone)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}

	prepareWith(RunOptions{})
	prepareWith(RunOptions{})
	pollute()
	prepareWith(RunOptions{})
	if cloneSize() == 0 {
		t.Fatal("cache hit at the current fingerprint must leave the clone untouched")
	}
	prepareWith(RunOptions{Fresh: true})
	if cloneSize() != 0 {
		t.Fatal("fresh prepare must restore the clone from the template")
	}
}
