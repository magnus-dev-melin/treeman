-- A worktree whose database teardown failed keeps the error here until
-- a retry succeeds. Deleted rows with a non-NULL teardown_error are the
-- daemon's retry queue; before this column a failed DROP was only an
-- event, the row was marked deleted anyway, and the namespaces leaked
-- for good (#171).
ALTER TABLE worktrees ADD COLUMN teardown_error TEXT;
CREATE INDEX idx_worktrees_teardown_pending ON worktrees(repo_id) WHERE teardown_error IS NOT NULL;
