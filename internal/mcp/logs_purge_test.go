package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

// A filter that resolves to nothing must never reach an unscoped
// DELETE FROM events: unknown levels and unknown repos used to be
// dropped after the "at least one filter" check.
func TestLogsPurgeRejectsFiltersThatWouldWiden(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	t.Setenv("TREEMAN_DB_PATH", dbPath)
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = st.WriteEvent(ctx, store.LevelInfo, "test", "keep me", 0, 0, "", 0, nil)
	}
	_ = st.Close()

	for _, tc := range []struct {
		name string
		in   logsPurgeIn
		want string
	}{
		{"unknown level", logsPurgeIn{Levels: []string{"warning"}, Ack: true}, `unknown level "warning"`},
		{"unresolvable repo", logsPurgeIn{Repo: t.TempDir(), Ack: true}, "resolve repo"},
		{"unenrolled repo", logsPurgeIn{Repo: enrolledRepoDir(t), Ack: true}, "not enrolled"},
		{"no filter", logsPurgeIn{Ack: true}, "refusing to purge every event"},
	} {
		_, out, err := logsPurgeTool(ctx, nil, tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v (out %+v), want it to contain %q", tc.name, err, out, tc.want)
		}
	}

	st, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if left, _ := st.QueryEvents(ctx, store.EventFilter{Limit: 10}); len(left) != 3 {
		t.Fatalf("%d event rows left, want 3", len(left))
	}
}
