-- The durable age sweep reads one repo's durables unused since a cutoff
-- (#172). (repo_id, last_used_at) serves that range and, through its
-- repo_id prefix, every repo-only lookup the old single-column index
-- served, so it replaces it.
DROP INDEX idx_branch_durables_repo;
CREATE INDEX idx_branch_durables_repo_lru ON branch_durables(repo_id, last_used_at);
