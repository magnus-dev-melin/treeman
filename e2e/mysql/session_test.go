//go:build e2e

package mysql_e2e

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stubbedev/treeman/e2e/harness"
	dbmysql "github.com/stubbedev/treeman/internal/db/mysql"
)

// Clone connections run with foreign_key_checks=0, unique_checks=0 and
// sql_log_bin=0. They used to go back into the driver's pool with those
// settings, so the next query on the same connection — a dump load, a
// migration, db_query — ran without FK checks and outside the binlog.
// With the pool capped at one connection, the query after a clone gets
// that very connection back unless the clone discarded it.
func TestCloneSessionSettingsDoNotLeakIntoThePool(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	harness.WaitForReady(t, "mysql:13306", 60*time.Second, func() error {
		c, err := net.DialTimeout("tcp", "127.0.0.1:13306", time.Second)
		if err != nil {
			return err
		}
		return c.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	drv, err := dbmysql.Connect(ctx, *buildConfig().Connections.Mysql)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = drv.Close() }()
	drv.DB.SetMaxOpenConns(1)

	for _, q := range []string{
		"DROP DATABASE IF EXISTS tm_sess_src", "CREATE DATABASE tm_sess_src",
		"CREATE TABLE tm_sess_src.t (id INT PRIMARY KEY)", "INSERT INTO tm_sess_src.t VALUES (1)",
		"CREATE VIEW tm_sess_src.v AS SELECT id FROM tm_sess_src.t",
	} {
		if _, err := drv.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = drv.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS tm_sess_src")
		_, _ = drv.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS tm_sess_tpl")
	})
	if err := drv.SnapshotCreate(ctx, "tm_sess_src", "tm_sess_tpl"); err != nil {
		t.Fatalf("snapshot create: %v", err)
	}

	var fk, uniq, binlog int
	var current any
	if err := drv.DB.QueryRowContext(ctx,
		"SELECT @@SESSION.foreign_key_checks, @@SESSION.unique_checks, @@SESSION.sql_log_bin, DATABASE()").
		Scan(&fk, &uniq, &binlog, &current); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || uniq != 1 || binlog != 1 || current != nil {
		t.Errorf("pooled connection after a clone: foreign_key_checks=%d unique_checks=%d sql_log_bin=%d database=%v; want 1 1 1 <nil>",
			fk, uniq, binlog, current)
	}
}
