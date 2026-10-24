package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// explain returns the EXPLAIN QUERY PLAN detail lines for q.
func explain(t *testing.T, st *Store, q string, args ...any) string {
	t.Helper()
	rows, err := st.DB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func assertServedWithoutSort(t *testing.T, st *Store, q string, args []any, index string) {
	t.Helper()
	plan := explain(t, st, q, args...)
	if !strings.Contains(plan, index) || strings.Contains(plan, "SCAN events") || strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("%s\nplan:\n%s\nwant it served by %s with no scan or sort", q, plan, index)
	}
}

func TestRepoScopedEventQueriesUseAnIndex(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	repoPage, repoArgs := queryEventsSQL(EventFilter{RepoID: 1, Limit: 50})
	assertServedWithoutSort(t, st, repoPage, repoArgs, "idx_events_repo_recent")
	assertServedWithoutSort(t, st, "DELETE FROM events WHERE repo_id = ?", []any{1}, "idx_events_repo_recent")
	assertServedWithoutSort(t, st, "DELETE FROM events WHERE worktree_id IN (SELECT id FROM worktrees WHERE repo_id = ?)", []any{1},
		"idx_events_worktree_recent")
}

// Two processes opening a fresh database right after an upgrade used
// to both see a migration as pending; the loser failed with
// "duplicate column" / "table already exists".
func TestConcurrentOpensApplyMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			st, err := Open(context.Background(), path)
			if err != nil {
				errs <- err
				return
			}
			errs <- st.Close()
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent open: %v", err)
		}
	}
}

func TestWorktreeLifecycleEventQueriesUseTheWorktreeIndexWithoutSorting(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	page, pageArgs := queryEventsSQL(EventFilter{WorktreeID: 1, EventTypes: WorktreeStateEventTypes, Limit: 1})
	assertServedWithoutSort(t, st, page, pageArgs, "idx_events_worktree_recent")

	latest, latestArgs := latestEventPerWorktreeSQL([]int64{1, 2, 3}, WorktreeStateEventTypes)
	assertServedWithoutSort(t, st, latest, latestArgs, "COVERING INDEX idx_events_worktree_recent")
}

func TestActiveWorktreePageUsesTheCoveringPartialIndexWithoutSorting(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	plan := explain(t, st, activeWorktreePageSQL, int64(0), 201)
	if !strings.Contains(plan, "COVERING INDEX idx_worktrees_active_page") || strings.Contains(plan, "SCAN") ||
		strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("plan:\n%s\nwant a covering idx_worktrees_active_page seek with no scan or sort", plan)
	}
}
