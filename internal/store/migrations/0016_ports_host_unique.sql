-- A TCP port is host-global, but uniqueness was enforced per (repo,
-- slot): two slots with overlapping ranges in one config, or two repos
-- sharing a range, were both handed the same port, and the servers
-- collided on bind. One live reservation per port number, whatever the
-- repo or slot. Duplicates the old index let through keep their oldest
-- row; the other worktrees get a fresh port on their next finalize.
DELETE FROM worktree_ports
WHERE id NOT IN (SELECT MIN(id) FROM worktree_ports GROUP BY port);
DROP INDEX idx_worktree_ports_one_per_port;
CREATE UNIQUE INDEX idx_worktree_ports_one_per_port ON worktree_ports(port);
