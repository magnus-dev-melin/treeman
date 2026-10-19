// Package dumpload streams a `.sql` dump into a target database. The
// native client (in the container or on PATH) is preferred; the wire
// fallback splits the dump statement by statement (see split.go) so
// the whole file is never buffered, following each dialect's lexing
// rules: MySQL's backslash escapes, DELIMITER directives and executable
// comments, Postgres's dollar quoting and COPY … FROM stdin blocks.
//
// Statements go through `database/sql`'s ExecContext, which speaks
// MySQL's text protocol — enough for LOCK TABLES and the other
// statements the prepared-statement protocol refuses. COPY data
// streams through pgx's CopyFrom.
package dumpload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/ident"
)

// LoadMySQL applies `dumpPath` to `targetDB` using the fastest
// available path. Strategy is selected in order:
//
//  1. `<container-engine> exec -i CID mysql …` — when `conn` is non-nil
//     and `conn.ContainerRef` resolves to a running container. Fastest
//     because the CLI talks to mysqld over an in-container unix socket.
//  2. Native `mysql` CLI on PATH piping the (decompressed) dump. Skips
//     the per-statement round-trips that the wire loader pays.
//  3. Wire-protocol streaming over the existing *sql.DB connection.
//     Always available, and the only path used when `conn` is nil.
//
// `conn` is optional: pass it so the fast paths can read host/port/
// user/password; pass nil to force the wire path (used by branch-
// scoped loadDump callers that only carry a *sql.DB).
//
// Returns the strategy that actually ran so callers can record it as
// an event. A fast-path FAILURE (exec ran but exited non-zero) is
// downgraded to a warning log + fall-through to the next strategy, so
// a misconfigured CLI on a dev box can never block a cold build.
//
// That fall-through is only safe while the failed attempt wrote NOTHING
// — the usual case (no container, `mysql` missing inside it, bad
// credentials). An attempt that died MID-DUMP has already applied part
// of the file, and replaying the same dump on top of it produced "Table
// 'x' already exists" or, worse, a target whose schema was complete but
// whose skipped INSERTs left the framework `migrations` table empty, so
// the following `migrate` replayed every migration and died on a
// duplicate column (issue #28). So the target is probed for new objects
// after a failed attempt: if it gained any, the error is returned
// instead of falling through, and the next prepare cold-builds the
// database from scratch.
//
// Compression (gzip/zstd/bzip2/xz) is auto-detected from the file's
// magic bytes — extension is not consulted.
func LoadMySQL(ctx context.Context, db *sql.DB, conn *config.MysqlConn, targetDB, dumpPath string) (LoadStrategy, error) {
	if err := ensureMySQLDB(ctx, db, targetDB); err != nil {
		return "", err
	}
	// Only the fast paths can leave a half-applied dump behind, and they
	// need `conn`; the wire-only path (conn == nil) skips the probe.
	countObjects := func() (int, bool) { return mysqlObjectCount(ctx, db, targetDB) }
	before, counted := 0, false
	if conn != nil {
		before, counted = countObjects()
	}
	if ok, err := runFastPathMySQL(ctx, conn, targetDB, dumpPath, tryDockerExecMySQL); ok {
		return StrategyDockerExec, nil
	} else if err != nil {
		if perr := partialLoadError("docker exec", dumpPath, targetDB, err, before, counted, countObjects); perr != nil {
			return "", perr
		}
	}
	if ok, err := runFastPathMySQL(ctx, conn, targetDB, dumpPath, tryNativeCLIMySQL); ok {
		return StrategyNativeCLI, nil
	} else if err != nil {
		if perr := partialLoadError("native CLI", dumpPath, targetDB, err, before, counted, countObjects); perr != nil {
			return "", perr
		}
	}
	return StrategyWire, loadMySQLViaWire(ctx, db, targetDB, dumpPath)
}

// partialLoadError decides what a failed fast-path attempt means. It
// returns nil when the attempt provably wrote nothing (the target holds
// the same object count as before it ran, or the count is unknown) — the
// caller then logs a warning and falls through to the next strategy, the
// long-standing "a misconfigured CLI can never block a cold build"
// behaviour. It returns an error when the target GAINED objects: the
// dump was applied in part, replaying it would collide or silently skip
// the aborted section's rows, and the honest move is to fail so the next
// prepare rebuilds the database from scratch (issue #28).
//
// ponytail: object count, not content. An attempt that died after
// INSERTing rows into existing tables without creating any is
// indistinguishable from one that wrote nothing, so a data-only dump can
// still be replayed on top of itself. Compare a row/checksum watermark
// per table if that ever bites.
func partialLoadError(
	strategy, dumpPath, targetDB string,
	attemptErr error,
	before int,
	counted bool,
	countObjects func() (int, bool),
) error {
	if counted {
		if after, ok := countObjects(); ok && after != before {
			return fmt.Errorf(
				"dump-load fast path (%s) aborted after applying part of %s: target %s gained %d object(s) "+
					"and replaying the dump on top would corrupt it; not falling through: %w",
				strategy, dumpPath, targetDB, after-before, attemptErr)
		}
	}
	slog.Warn("dump-load fast path failed; falling through",
		"strategy", strategy, "target", targetDB, "error", attemptErr)
	return nil
}

// mysqlObjectCount counts tables + views in `targetDB`. The bool is
// false when the count could not be taken (nil handle, query error), in
// which case callers keep the old fall-through behaviour rather than
// blocking a cold build on a probe failure.
func mysqlObjectCount(ctx context.Context, db *sql.DB, targetDB string) (int, bool) {
	if db == nil {
		return 0, false
	}
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?",
		targetDB).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// postgresObjectCount mirrors mysqlObjectCount for a *sql.DB already
// scoped to the target database (pg has no USE), counting every
// non-system schema so a dump that creates its own schemas is covered.
func postgresObjectCount(ctx context.Context, db *sql.DB) (int, bool) {
	if db == nil {
		return 0, false
	}
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables "+
			"WHERE table_schema NOT IN ('pg_catalog', 'information_schema')").Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// LoadPostgres mirrors LoadMySQL for pgx-backed databases. The `db`
// arg must be a connection scoped to `targetDB` (pg has no USE); the
// wire-protocol fallback uses it directly. The fast paths consult
// `conn` and reconnect via the CLI.
func LoadPostgres(ctx context.Context, db *sql.DB, conn *config.PostgresConn, targetDB, dumpPath string) (LoadStrategy, error) {
	// See LoadMySQL: probe only when a fast path can actually run.
	countObjects := func() (int, bool) { return postgresObjectCount(ctx, db) }
	before, counted := 0, false
	if conn != nil {
		before, counted = countObjects()
	}
	if ok, err := runFastPathPostgres(ctx, conn, targetDB, dumpPath, tryDockerExecPostgres); ok {
		return StrategyDockerExec, nil
	} else if err != nil {
		if perr := partialLoadError("docker exec", dumpPath, targetDB, err, before, counted, countObjects); perr != nil {
			return "", perr
		}
	}
	if ok, err := runFastPathPostgres(ctx, conn, targetDB, dumpPath, tryNativeCLIPostgres); ok {
		return StrategyNativeCLI, nil
	} else if err != nil {
		if perr := partialLoadError("native CLI", dumpPath, targetDB, err, before, counted, countObjects); perr != nil {
			return "", perr
		}
	}
	return StrategyWire, loadPostgresViaWire(ctx, db, dumpPath)
}

// runFastPathMySQL opens the dump (with compression sniffing), hands
// the decompressed reader to `attempt`, and forwards the (ok, err)
// triple back to the dispatcher. Centralises file open + close so
// each tryXxx helper stays focused on the exec mechanics.
func runFastPathMySQL(ctx context.Context, conn *config.MysqlConn, targetDB, dumpPath string,
	attempt func(context.Context, *config.MysqlConn, string, io.Reader) (bool, error),
) (bool, error) {
	if conn == nil {
		return false, nil
	}
	f, _, err := OpenDump(dumpPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return attempt(ctx, conn, targetDB, f)
}

// runFastPathPostgres mirrors runFastPathMySQL for the psql helpers.
func runFastPathPostgres(ctx context.Context, conn *config.PostgresConn, targetDB, dumpPath string,
	attempt func(context.Context, *config.PostgresConn, string, io.Reader) (bool, error),
) (bool, error) {
	if conn == nil {
		return false, nil
	}
	f, _, err := OpenDump(dumpPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return attempt(ctx, conn, targetDB, f)
}

// loadMySQLViaWire is the original statement-streaming loader. Uses
// streamStatements internally so a 10GB dump is bounded by the largest
// single statement size (typically ~64MB for mysqldump extended-
// inserts), not the file size.
func loadMySQLViaWire(ctx context.Context, db *sql.DB, targetDB, dumpPath string) error {
	f, _, err := OpenDump(dumpPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("USE `%s`", targetDB)); err != nil {
		return fmt.Errorf("USE `%s`: %w", targetDB, err)
	}
	for _, s := range []string{
		"SET SESSION foreign_key_checks=0",
		"SET SESSION unique_checks=0",
		"SET SESSION sql_log_bin=0",
	} {
		_, _ = conn.ExecContext(ctx, s) // best-effort; some flags need SUPER
	}
	return streamStatements(ctx, f, dialectMySQL, statementSink{onStmt: retryingExec(ctx, func(stmt string) error {
		_, err := conn.ExecContext(ctx, stmt)
		return err
	})})
}

// ensureMySQLDB creates `targetDB` when it is missing, so a dump load
// never dies on `USE …: Error 1049 Unknown database`. A prepare that
// fails between cold-build's DROP DATABASE and the dump load leaves the
// source gone while SQLite still holds a snapshot row for it; before
// this, every retry failed on the missing database and the only way out
// was a manual `snapshots_drop` (#24). Idempotent, and the DDL matches
// mysql.Driver.EnsureDB so a DB created here is not charset-divergent.
func ensureMySQLDB(ctx context.Context, db *sql.DB, targetDB string) error {
	if db == nil {
		return nil
	}
	qname, err := ident.QuoteMySQL(targetDB)
	if err != nil {
		return err
	}
	//nolint:gosec // qname is validated/quoted via ident.QuoteMySQL; no user values concatenated
	stmt := "CREATE DATABASE IF NOT EXISTS " + qname +
		" DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("CREATE DATABASE %s: %w", qname, err)
	}
	return nil
}

// stmtLockRetries / stmtLockBackoff bound the per-statement retry of an
// InnoDB lock conflict. Contention comes from sibling worktrees loading
// dumps into their own databases at the same time; the loser is told to
// restart its transaction and usually wins within a few hundred ms.
const stmtLockRetries = 4

var stmtLockBackoff = []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second}

// retryingExec wraps a per-statement exec so a deadlock (1213) or lock-
// wait timeout (1205) retries instead of aborting the whole dump load
// — both are explicitly restartable, and losing a 10-minute load to one
// of them left the worktree unpreparable (#24).
//
// Retrying is only sound under autocommit, where the rolled-back
// transaction is the single failed statement. Dumps that open an
// explicit transaction (`BEGIN` / `START TRANSACTION`) get no retry:
// InnoDB rolled back everything since the BEGIN, so re-running just the
// last statement would silently commit a partial transaction.
func retryingExec(ctx context.Context, exec func(stmt string) error) func(stmt string) error {
	inTx := false
	return func(stmt string) error {
		for attempt := 0; ; attempt++ {
			err := exec(stmt)
			if err == nil {
				inTx = txStateAfter(inTx, stmt)
				return nil
			}
			if inTx || attempt >= stmtLockRetries || !isLockContention(err) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(stmtLockBackoff[min(attempt, len(stmtLockBackoff)-1)]):
			}
		}
	}
}

// txStateAfter reports whether an explicit transaction is open after
// `stmt` ran. Only the statements mysqldump emits are recognised;
// anything else leaves the state unchanged.
func txStateAfter(inTx bool, stmt string) bool {
	s := strings.ToUpper(strings.TrimLeft(stmt, " \t\r\n"))
	switch {
	case strings.HasPrefix(s, "BEGIN"), strings.HasPrefix(s, "START TRANSACTION"):
		return true
	case strings.HasPrefix(s, "COMMIT"), strings.HasPrefix(s, "ROLLBACK"):
		return false
	}
	return inTx
}

// isLockContention reports whether err is an InnoDB deadlock (1213) or
// lock-wait timeout (1205) — the two errors MySQL itself documents as
// "try restarting transaction".
func isLockContention(err error) bool {
	var me *gomysql.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	return me.Number == 1213 || me.Number == 1205
}

// loadPostgresViaWire is the original statement-streaming loader.
// Caller must have opened db against the target database (pg has no
// USE), which it always does (preparePostgres uses OpenScoped).
func loadPostgresViaWire(ctx context.Context, db *sql.DB, dumpPath string) error {
	f, _, err := OpenDump(dumpPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return streamStatements(ctx, f, dialectPostgres, statementSink{
		onStmt: func(stmt string) error {
			_, err := conn.ExecContext(ctx, stmt)
			return err
		},
		onCopy: func(stmt string, data io.Reader) error {
			return conn.Raw(func(dc any) error {
				pc, ok := dc.(*stdlib.Conn)
				if !ok {
					return fmt.Errorf("COPY needs a pgx connection, got %T", dc)
				}
				_, err := pc.Conn().PgConn().CopyFrom(ctx, data, stmt)
				return err
			})
		},
	})
}
