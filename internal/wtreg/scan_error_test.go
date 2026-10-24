package wtreg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func TestLoadRepairRowsFailsOnAnUnreadableRow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/r", "r")
	if _, err := st.DB.ExecContext(
		ctx,
		`INSERT INTO worktrees(repo_id, path, slug, created_at, is_main) VALUES (?, '/r/.wt/x', 'x', 0, 'not-a-bool')`,
		repoID,
	); err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if rows, err := loadRepairRows(ctx, tx, repoID); err == nil {
		t.Fatalf("an unreadable worktree row was skipped instead of failing the repair: %v", rows)
	}
}
