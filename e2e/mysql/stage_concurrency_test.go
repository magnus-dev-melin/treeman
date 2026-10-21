//go:build e2e

package mysql_e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	dbmysql "github.com/stubbedev/treeman/internal/db/mysql"
)

const stageRoot = "/var/lib/mysql-files/_tm_stage"

func assertNoStageLeft(t *testing.T, template string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", "treeman-e2e-mysql",
		"sh", "-c", "ls -A "+stageRoot+"/"+template+" 2>/dev/null").CombinedOutput()
	if listing := strings.TrimSpace(string(out)); err == nil && listing != "" {
		t.Errorf("staging dirs left behind for %s: %q", template, listing)
	}
}

func TestConcurrentStagedRestoresFromSeparateDrivers(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Setenv("TREEMAN_MYSQL_PHYSICAL_MIN_BYTES", "0")
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	harness.WaitForReady(t, "mysql:13306", 60*time.Second, func() error {
		c, err := net.DialTimeout("tcp", "127.0.0.1:13306", 1*time.Second)
		if err != nil {
			return err
		}
		_ = c.Close()
		return nil
	})

	const (
		nTables         = 40
		rowsEach        = 50
		nDrivers        = 4
		clonesPerDriver = 3
	)
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "stage.sql"), []byte(genBeefySeed(nTables, rowsEach)), 0o644); err != nil {
		t.Fatal(err)
	}
	conn := config.MysqlConn{
		Host: "127.0.0.1", Port: 3306, User: "root", Password: "rootpw",
		ContainerRef: config.ContainerRef{Container: "treeman-e2e-mysql"},
	}
	cfg := &config.Config{
		Connections: config.ConnectionsConfig{Mysql: &conn},
		Databases: []config.DatabaseConfig{{
			Engine:       "mysql",
			NameTemplate: "treeman_stage_{slug}",
			Dump:         config.DumpList{{Path: "stage.sql"}},
		}},
	}
	o := harness.AssertOutcome(t, harness.NewEnv(t, wt).RunPrepare(t, cfg), "mysql", false)

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, nDrivers*clonesPerDriver)
	var targets []string
	for drv := range nDrivers {
		for c := range clonesPerDriver {
			targets = append(targets, fmt.Sprintf("treeman_stage_d%d_c%d", drv, c))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			driver, err := dbmysql.Connect(ctx, conn)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = driver.Close() }()
			var inner sync.WaitGroup
			for c := range clonesPerDriver {
				inner.Add(1)
				go func() {
					defer inner.Done()
					target := fmt.Sprintf("treeman_stage_d%d_c%d", drv, c)
					if err := driver.SnapshotRestoreStaged(ctx, o.TemplateName, target); err != nil {
						errs <- fmt.Errorf("restore %s: %w", target, err)
					}
				}()
			}
			inner.Wait()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	wantTables := make([]string, nTables)
	for i := range wantTables {
		wantTables[i] = fmt.Sprintf("t%02d", i)
	}
	for _, target := range targets {
		assertTablesPresent(t, "127.0.0.1:13306", target, wantTables)
		for _, tbl := range []string{"t00", fmt.Sprintf("t%02d", nTables/2), fmt.Sprintf("t%02d", nTables-1)} {
			if got := rowCount(t, "127.0.0.1:13306", target, tbl); got != rowsEach {
				t.Errorf("%s.%s rows = %d, want %d", target, tbl, got, rowsEach)
			}
		}
	}
	assertNoStageLeft(t, o.TemplateName)
}
