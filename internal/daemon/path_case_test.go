package daemon

import (
	"context"
	"slices"
	"testing"
)

func TestDaemonPathLookupsMatchCaseVariantsLikeEnsureWorktree(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()
	repoID, err := st.Store.EnsureRepo(ctx, "/src/MyApp", "myapp")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.Store.EnsureWorktree(ctx, repoID, "/src/MyApp/.worktrees/Feat", "feat", "feat")
	if err != nil {
		t.Fatal(err)
	}
	row, err := lookupWorktreeByPath(ctx, st.Store, "/src/myapp/.worktrees/feat")
	if err != nil || row.ID != wtID {
		t.Fatalf("lookupWorktreeByPath(case variant) = %+v, %v; want worktree %d", row, err, wtID)
	}
	paths, err := listWorktreePaths(ctx, st, "/src/myapp")
	if err != nil || !slices.Equal(paths, []string{"/src/MyApp/.worktrees/Feat"}) {
		t.Fatalf("listWorktreePaths(case variant of the repo) = %v, %v", paths, err)
	}
}
