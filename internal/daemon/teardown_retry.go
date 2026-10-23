package daemon

import (
	"context"
	"log/slog"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/prepare"
	"github.com/stubbedev/treeman/internal/store"
)

// retryPendingTeardowns re-runs the database teardown of every deleted
// worktree in repoID whose earlier teardown failed (a server at its
// connection limit, an engine that was down). Teardown is idempotent,
// so namespaces the first attempt did drop are no-ops. A success clears
// the worktree's teardown_error; a failure keeps it for the next sweep.
// Only the databases are retried: the worktree's hooks already ran and
// its checkout is gone.
func retryPendingTeardowns(ctx context.Context, st *State, cfg *config.Config, repoID int64) {
	if len(cfg.Databases) == 0 {
		return
	}
	pending, err := st.Store.ListPendingTeardowns(ctx, repoID)
	if err != nil {
		slog.Warn("teardown retry: list pending", "repo_id", repoID, "err", err)
		return
	}
	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		retryTeardown(ctx, st, cfg, p)
	}
}

func retryTeardown(ctx context.Context, st *State, cfg *config.Config, p store.PendingTeardown) {
	if !st.MarkTeardownInFlight(p.Path) {
		return
	}
	defer st.UnmarkTeardownInFlight(p.Path)

	err := prepare.TeardownDatabases(ctx, cfg, p.Slug, p.RepoID, p.WorktreeID, st.Store)
	payload := map[string]string{"slug": p.Slug, "previous_error": p.Error}
	if err != nil {
		payload["error"] = err.Error()
		_ = st.Store.WriteEvent(ctx, store.LevelWarn, store.EvtDBTeardownRetry,
			"retried database teardown of deleted worktree "+p.Path+"; still failing: "+err.Error(),
			p.RepoID, p.WorktreeID, "", 0, payload)
		return
	}
	_ = st.Store.WriteEvent(ctx, store.LevelInfo, store.EvtDBTeardownRetry,
		"retried database teardown of deleted worktree "+p.Path+"; databases dropped",
		p.RepoID, p.WorktreeID, "", 0, payload)
}
