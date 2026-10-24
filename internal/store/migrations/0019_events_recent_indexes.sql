DROP INDEX idx_events_worktree;
DROP INDEX idx_events_repo;
CREATE INDEX idx_events_worktree_recent ON events(worktree_id, ts DESC, id DESC, event_type);
CREATE INDEX idx_events_repo_recent ON events(repo_id, ts DESC, id DESC);
