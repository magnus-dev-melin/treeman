//go:build e2e

// Package db_query_e2e drives the MCP db_query tool against real MySQL
// and Postgres and proves its read-only mode is enforced by the engine:
// statements that pass the leading-keyword check but modify data must
// fail and leave the rows in place.
package db_query_e2e

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/stubbedev/treeman/e2e/harness"
)

const (
	mysqlDSN    = "root:rootpw@tcp(127.0.0.1:13476)/"
	postgresDSN = "postgres://postgres:pgpw@127.0.0.1:15572/postgres?sslmode=disable"
)

func TestDBQueryReadOnlyIsEnforcedByTheEngine(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	mysqlDB := openReady(t, "mysql", mysqlDSN)
	pgDB := openReady(t, "pgx", postgresDSN)

	mustExec(t, mysqlDB, "CREATE DATABASE app")
	mustExec(t, mysqlDB, "CREATE TABLE app.t (id INT PRIMARY KEY)")
	mustExec(t, mysqlDB, "INSERT INTO app.t VALUES (1),(2),(3)")
	mustExec(t, pgDB, "CREATE TABLE t (id INT PRIMARY KEY)")
	mustExec(t, pgDB, "INSERT INTO t VALUES (1),(2),(3)")

	repo := t.TempDir()
	harness.WriteFile(t, repo, ".treeman.yaml", `
connections:
  mysql: {host: 127.0.0.1, port: 13476, user: root, password: rootpw}
  postgres: {host: 127.0.0.1, port: 15572, user: postgres, password: pgpw}
`)
	t.Setenv("TREEMAN_DB_PATH", filepath.Join(t.TempDir(), "treeman.db"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TREEMAN_MCP_ALL_TOOLS", "1")
	srv := harness.StartMCPServer(t, repo)

	query := func(engine, db, q string) (map[string]any, string) {
		return srv.CallTool("db_query", map[string]any{"repo": repo, "engine": engine, "db": db, "query": q})
	}

	if out, errText := query("mysql", "app", "SELECT id FROM t ORDER BY id"); errText != "" || len(out["rows"].([]any)) != 3 {
		t.Fatalf("mysql read: out=%v err=%s", out, errText)
	}
	if out, errText := query("postgres", "postgres", "SELECT id FROM t ORDER BY id"); errText != "" || len(out["rows"].([]any)) != 3 {
		t.Fatalf("postgres read: out=%v err=%s", out, errText)
	}

	for _, tc := range []struct {
		engine, db, q string
		check         *sql.DB
		table         string
	}{
		{"mysql", "app", "WITH x AS (SELECT 1) DELETE FROM t WHERE id IN (SELECT * FROM x)", mysqlDB, "app.t"},
		{"postgres", "postgres", "WITH d AS (DELETE FROM t RETURNING *) SELECT count(*) FROM d", pgDB, "t"},
		{"postgres", "postgres", "EXPLAIN ANALYZE DELETE FROM t", pgDB, "t"},
	} {
		_, errText := query(tc.engine, tc.db, tc.q)
		if !strings.Contains(strings.ToLower(errText), "read") {
			t.Errorf("%s %q: err = %q, want a read-only refusal", tc.engine, tc.q, errText)
		}
		var n int
		if err := tc.check.QueryRowContext(context.Background(), "SELECT count(*) FROM "+tc.table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("%s %q deleted rows in read-only mode: %d left, want 3", tc.engine, tc.q, n)
		}
	}
}

func openReady(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	harness.WaitForReady(t, driver, 90*time.Second, func() error { return db.Ping() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
