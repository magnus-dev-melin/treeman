CREATE INDEX idx_worktrees_active_page ON worktrees(id, repo_id, slug, branch, path, is_main, deleted_at) WHERE deleted_at IS NULL;
