//go:build e2e

// Package dumpload_e2e loads mysqldump- and pg_dump-shaped dumps
// through the wire loaders (no connection config, so no native client
// fast path) into real servers, proving the statement splitter follows
// each dialect: MySQL backslash escapes, DELIMITER blocks and
// executable-comment triggers; Postgres dollar-quoted bodies and
// COPY … FROM stdin blocks.
package dumpload_e2e

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/db/dumpload"
)

const mysqlDump = `-- MySQL dump
/*!40101 SET NAMES utf8mb4 */;
CREATE TABLE t (id INT PRIMARY KEY, s VARCHAR(50));
CREATE TABLE audit (id INT);
INSERT INTO t VALUES (1,'O\'Brien'),(2,'a;b'),(3,'&amp;');
DELIMITER ;;
/*!50003 CREATE*/ /*!50003 TRIGGER t_ai AFTER INSERT ON t FOR EACH ROW BEGIN
  INSERT INTO audit VALUES (NEW.id);
  INSERT INTO audit VALUES (NEW.id);
END */;;
DELIMITER ;
`

const postgresDump = `CREATE TABLE t (id int PRIMARY KEY, s text);
CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END; $$;
COPY public.t (id, s) FROM stdin;
1	a;b
2	\N
\.
SELECT f();
`

func TestWireLoadersFollowEachDialect(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	ctx := context.Background()
	dir := t.TempDir()

	my := openReady(t, "mysql", "root:rootpw@tcp(127.0.0.1:13486)/?multiStatements=false")
	strategy, err := dumpload.LoadMySQL(ctx, my, nil, "app", write(t, dir, "my.sql", mysqlDump))
	if err != nil {
		t.Fatalf("mysql wire load: %v", err)
	}
	if strategy != dumpload.StrategyWire {
		t.Fatalf("mysql strategy = %s, want wire", strategy)
	}
	var s1, s2, s3 string
	if err := my.QueryRowContext(ctx, "SELECT (SELECT s FROM app.t WHERE id=1),(SELECT s FROM app.t WHERE id=2),(SELECT s FROM app.t WHERE id=3)").
		Scan(&s1, &s2, &s3); err != nil {
		t.Fatal(err)
	}
	if s1 != "O'Brien" || s2 != "a;b" || s3 != "&amp;" {
		t.Errorf("mysql rows = %q %q %q", s1, s2, s3)
	}
	var triggers int
	if err := my.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='app'").
		Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 1 {
		t.Errorf("mysql triggers = %d, want 1 (the DELIMITER block was not applied)", triggers)
	}

	pg := openReady(t, "pgx", "postgres://postgres:pgpw@127.0.0.1:15582/postgres?sslmode=disable")
	strategy, err = dumpload.LoadPostgres(ctx, pg, nil, "postgres", write(t, dir, "pg.sql", postgresDump))
	if err != nil {
		t.Fatalf("postgres wire load: %v", err)
	}
	if strategy != dumpload.StrategyWire {
		t.Fatalf("postgres strategy = %s, want wire", strategy)
	}
	var rows, nulls, f int
	if err := pg.QueryRowContext(ctx, "SELECT count(*), count(*) FILTER (WHERE s IS NULL), f() FROM t").
		Scan(&rows, &nulls, &f); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || nulls != 1 || f != 1 {
		t.Errorf("postgres rows=%d nulls=%d f()=%d, want 2 1 1", rows, nulls, f)
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
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
