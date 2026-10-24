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

// Repo-scoped event reads and the repo teardown deletes must be served
// by an index: the event log grows without bound, and a full scan ran
// for every repo removal (FK check included) and repo-filtered query.
func TestRepoScopedEventQueriesUseAnIndex(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	where, args := queryEventsWhere(EventFilter{RepoID: 1})
	for _, tc := range []struct {
		q     string
		args  []any
		index string
	}{
		{"SELECT e.id FROM events e WHERE " + strings.Join(where, " AND ") + " ORDER BY e.ts DESC, e.id DESC LIMIT 50", args, "idx_events_repo"},
		{"DELETE FROM events WHERE repo_id = ?", []any{1}, "idx_events_repo"},
		{"DELETE FROM events WHERE worktree_id IN (SELECT id FROM worktrees WHERE repo_id = ?)", []any{1}, "idx_events_worktree"},
	} {
		plan := explain(t, st, tc.q, tc.args...)
		if !strings.Contains(plan, tc.index) || strings.Contains(plan, "SCAN events") {
			t.Errorf("%s\nplan:\n%s\nwant it served by %s", tc.q, plan, tc.index)
		}
	}
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

func TestWorktreeLifecycleEventQueryUsesTheWorktreeIndex(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	where, args := queryEventsWhere(EventFilter{WorktreeID: 1, EventTypes: WorktreeStateEventTypes})
	q := "SELECT e.id FROM events e WHERE " + strings.Join(where, " AND ") + " ORDER BY e.ts DESC, e.id DESC LIMIT 1"
	plan := explain(t, st, q, args...)
	if !strings.Contains(plan, "idx_events_worktree") || strings.Contains(plan, "SCAN events") {
		t.Errorf("%s\nplan:\n%s\nwant it served by idx_events_worktree", q, plan)
	}
}
