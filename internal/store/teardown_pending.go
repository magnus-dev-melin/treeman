package store

import "context"

// PendingTeardown is a deleted worktree whose database teardown failed
// and has not succeeded since.
type PendingTeardown struct {
	WorktreeID int64
	RepoID     int64
	Path       string
	Slug       string
	Error      string
}

// SetTeardownError records that dropping the worktree's databases
// failed with msg, queueing the worktree for the daemon's retry sweep.
func (s *Store) SetTeardownError(ctx context.Context, worktreeID int64, msg string) error {
	_, err := s.DB.ExecContext(ctx,
		"UPDATE worktrees SET teardown_error = ? WHERE id = ?", msg, worktreeID)
	return err
}

// ClearTeardownError marks the worktree's database teardown as done.
func (s *Store) ClearTeardownError(ctx context.Context, worktreeID int64) error {
	_, err := s.DB.ExecContext(ctx,
		"UPDATE worktrees SET teardown_error = NULL WHERE id = ? AND teardown_error IS NOT NULL", worktreeID)
	return err
}

// ListPendingTeardowns returns repoID's deleted worktrees whose
// database teardown is still outstanding, oldest first. A worktree
// whose slug a live worktree has since taken is left out: its
// namespaces belong to the live one now, and dropping them would wipe
// its data.
func (s *Store) ListPendingTeardowns(ctx context.Context, repoID int64) ([]PendingTeardown, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT w.id, w.repo_id, w.path, w.slug, w.teardown_error
		FROM worktrees w
		WHERE w.repo_id = ? AND w.teardown_error IS NOT NULL AND w.deleted_at IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM worktrees l
			WHERE l.slug = w.slug AND l.repo_id = w.repo_id AND l.deleted_at IS NULL)
		ORDER BY w.id`, repoID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PendingTeardown
	for rows.Next() {
		var p PendingTeardown
		if err := rows.Scan(&p.WorktreeID, &p.RepoID, &p.Path, &p.Slug, &p.Error); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
