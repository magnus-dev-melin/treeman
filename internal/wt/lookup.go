package wt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stubbedev/treeman/internal/gitenv"
	"github.com/stubbedev/treeman/internal/store"
)

// LookupWorktree queries SQLite for active worktrees attached to
// repoRoot and returns the path that best matches name:
//
//  1. exact basename match
//  2. exact branch match
//  3. exact slug match
//  4. unambiguous prefix match (basename or slug or branch)
//
// Multiple prefix hits are surfaced via the sink so the user can
// disambiguate; the function returns ("", false) in that case.
func LookupWorktree(ctx context.Context, repoRoot, name string, sink Sink) (string, bool, error) {
	if sink == nil {
		sink = NoopSink{}
	}
	st, err := openSharedDefaultStore(ctx)
	if err != nil {
		return "", false, err
	}
	//nolint:gosec // WorktreeNotTearingDown is a compile-time constant fragment
	rows, err := st.DB.QueryContext(ctx, `
		SELECT w.path, w.slug, COALESCE(w.branch,'')
		FROM worktrees w JOIN repos r ON r.id = w.repo_id
		WHERE w.deleted_at IS NULL AND r.path = ? COLLATE NOCASE
		AND `+store.WorktreeNotTearingDown, repoRoot)
	if err != nil {
		return "", false, fmt.Errorf("look up worktree %q in %s: %w", name, repoRoot, err)
	}
	defer func() { _ = rows.Close() }()
	var exactBase, exactSlug, exactBranch string
	var prefixHits []string
	for rows.Next() {
		var path, slug, branch string
		if err := rows.Scan(&path, &slug, &branch); err != nil {
			return "", false, fmt.Errorf("look up worktree %q in %s: %w", name, repoRoot, err)
		}
		base := filepath.Base(path)
		switch {
		case base == name:
			exactBase = path
		case slug == name:
			exactSlug = path
		case branch == name:
			exactBranch = path
		case len(name) >= 2 && (strings.HasPrefix(base, name) || strings.HasPrefix(slug, name) || strings.HasPrefix(branch, name)):
			prefixHits = append(prefixHits, path)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("look up worktree %q in %s: %w", name, repoRoot, err)
	}
	switch {
	case exactBase != "":
		return exactBase, true, nil
	case exactBranch != "":
		return exactBranch, true, nil
	case exactSlug != "":
		return exactSlug, true, nil
	case len(prefixHits) == 1:
		return prefixHits[0], true, nil
	case len(prefixHits) > 1:
		sink.Warn("ambiguous prefix match — candidates:")
		for _, p := range prefixHits {
			sink.Warn("  %s", p)
		}
	}
	return "", false, nil
}

func IsMatchingExistingWorktree(ctx context.Context, repoRoot, wtPath, branch string) (bool, error) {
	if !gitenv.IsLinkedWorktree(wtPath) {
		return false, nil
	}
	if current := gitenv.DetectBranch(ctx, wtPath); current != branch {
		return false, nil
	}
	st, err := openSharedDefaultStore(ctx)
	if err != nil {
		return false, err
	}
	var n int
	err = st.DB.QueryRowContext(ctx,
		`SELECT 1 FROM worktrees w JOIN repos r ON r.id = w.repo_id
		 WHERE r.path = ? COLLATE NOCASE AND w.path = ? COLLATE NOCASE AND w.deleted_at IS NULL`,
		repoRoot, wtPath).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check the registry for worktree %s: %w", wtPath, err)
	}
	return true, nil
}

func openSharedDefaultStore(ctx context.Context) (*store.Store, error) {
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve the treeman store path: %w", err)
	}
	st, err := store.OpenShared(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open the treeman store at %s: %w", dbPath, err)
	}
	return st, nil
}
