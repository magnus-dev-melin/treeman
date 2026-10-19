-- events.repo_id is a foreign key to repos(id) with no index: every
-- DELETE FROM repos (RemoveRepo, the boot-time dead-repo reap) made
-- SQLite's FK check scan the whole event log, and so did repo-scoped
-- log queries and purges. (repo_id, ts DESC) also serves
-- QueryEvents' `WHERE repo_id = ? ORDER BY ts DESC`.
CREATE INDEX IF NOT EXISTS idx_events_repo ON events(repo_id, ts DESC);
