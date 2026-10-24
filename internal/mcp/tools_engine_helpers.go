package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/klauspost/compress/gzip"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	dbes "github.com/stubbedev/treeman/internal/db/es"
	dbmongo "github.com/stubbedev/treeman/internal/db/mongo"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/snapshot"
	"github.com/stubbedev/treeman/internal/store"
)

// Alias so tools_engine.go can refer to the snapshot type without
// pulling the store import into its own header — keeps the engine
// tools file focused on MCP wiring.
type storeSnapshotRecord = store.SnapshotRecord

func snapshotLookupByFingerprint(ctx context.Context, st *store.Store, fp string) (*store.SnapshotRecord, error) {
	return st.LookupSnapshot(ctx, fp)
}

// snapshotLookupByEngineSource returns the most-recently-used
// snapshot matching (engine, source_db). Slow path compared to
// fingerprint lookup, but useful when the caller only knows the
// engine+source pair (e.g. the agent saw the names in YAML).
func snapshotLookupByEngineSource(ctx context.Context, st *store.Store, engine, sourceDB string) (*store.SnapshotRecord, error) {
	var fp string
	err := st.DB.QueryRowContext(ctx, `
		SELECT fingerprint FROM snapshots
		WHERE engine = ? AND source_db = ?
		ORDER BY last_used_at DESC LIMIT 1
	`, engine, sourceDB).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return st.LookupSnapshot(ctx, fp)
}

func snapshotRecordToMap(r *store.SnapshotRecord) map[string]any {
	return map[string]any{
		"fingerprint":     r.Fingerprint,
		"engine":          r.Engine,
		"engine_version":  r.EngineVersion,
		"source_db":       r.SourceDB,
		"template_name":   r.TemplateName,
		"migrations_hash": r.MigrationsHash,
		"dump_hash":       r.DumpHash,
		"lockfile_hashes": r.LockfileHashes,
		"repo_id":         r.RepoID,
		"size_bytes":      r.SizeBytes,
		"created_at":      r.CreatedAt,
		"last_used_at":    r.LastUsedAt,
		"use_count":       r.UseCount,
	}
}

type templateProbe struct {
	exists  bool
	sizeKB  int64
	version string
}

func probeTemplate(ctx context.Context, cfg *config.Config, eng, connName, template string) (templateProbe, error) {
	fam, ok := engine.Canonical(eng)
	if !ok {
		return templateProbe{}, fmt.Errorf("template %s: unknown engine %q", template, eng)
	}
	conn, configured, err := engineconn.Connect(ctx, cfg, fam, connName)
	if err != nil {
		return templateProbe{}, fmt.Errorf("connect to %s to probe template %s: %w", eng, template, err)
	}
	if !configured {
		return templateProbe{}, fmt.Errorf("probe template %s: no %s connection %q is configured", template, eng, connName)
	}
	defer func() { _ = conn.Close() }()
	exists, err := conn.Exists(ctx, template)
	if err != nil {
		return templateProbe{}, fmt.Errorf("check whether %s template %s exists: %w", eng, template, err)
	}
	ver, err := conn.EngineVersion(ctx)
	if err != nil {
		return templateProbe{}, fmt.Errorf("read the %s engine version: %w", eng, err)
	}
	p := templateProbe{exists: exists, version: ver}
	if exists {
		p.sizeKB = conn.SizeKB(ctx, template)
	}
	return p, nil
}

// reservedTemplatePrefixes are the namespace markers treeman owns:
// cache templates (`_tm_<hex>`, `_tm:<hex>:` for Redis, `tm_<hex>` for
// ES, `_tm_<hex><ext>` file names for the file engines) and
// branch-scoped durable copies (`_tmbs_<hex>` / `tmbs_<hex>` for ES).
// MCP snapshots_drop refuses anything else, so a hand-crafted SQLite
// row can't point the drop at an app database.
var reservedTemplatePrefixes = []string{"_tm_", "_tm:", "_tmbs_", "tm_", "tmbs_"}

// isReservedTemplateName checks the name's last path element, so a
// file-engine template's absolute path qualifies by its file name.
func isReservedTemplateName(name string) bool {
	base := filepath.Base(name)
	for _, p := range reservedTemplatePrefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	return false
}

// dropTemplate removes the engine-side template for one snapshot row
// through snapshot.DropTemplate — the same drop GC uses, on the server
// the row names — after refusing any name that doesn't carry a
// treeman-reserved prefix.
func dropTemplate(ctx context.Context, cfg *config.Config, c store.SnapshotEvictionCandidate) error {
	if !isReservedTemplateName(c.TemplateName) {
		return fmt.Errorf(
			"refusing to drop %q: not a treeman-reserved template name (expected one of %v)",
			c.TemplateName,
			reservedTemplatePrefixes,
		)
	}
	return snapshot.DropTemplate(ctx, cfg, c)
}

// ─── dump generators ────────────────────────────────────────────────

func runMysqldump(
	ctx context.Context,
	conn *config.MysqlConn,
	db, outPath string,
	gzipOut bool,
) (*mcpsdk.CallToolResult, dbDumpOut, error) {
	if _, err := exec.LookPath("mysqldump"); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("mysqldump not on PATH: %w", err)
	}
	args := []string{
		"--host=" + conn.Host,
		"--port=" + strconv.Itoa(coalescePort(conn.Port, 3306)),
		"--user=" + conn.User,
		"--single-transaction", "--routines", "--triggers",
		db,
	}
	cmd := exec.CommandContext(ctx, "mysqldump", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+conn.Password)
	return runDumpCmd(cmd, outPath, gzipOut)
}

func runPgDump(
	ctx context.Context,
	conn *config.PostgresConn,
	db, outPath string,
	gzipOut bool,
) (*mcpsdk.CallToolResult, dbDumpOut, error) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("pg_dump not on PATH: %w", err)
	}
	args := []string{
		"--host=" + conn.Host,
		"--port=" + strconv.Itoa(coalescePort(conn.Port, 5432)),
		"--username=" + conn.User,
		"--no-password", "--format=plain", "--clean", "--if-exists", db,
	}
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password)
	return runDumpCmd(cmd, outPath, gzipOut)
}

// runESDump writes a `_bulk`-format NDJSON dump of every doc under
// `<prefix>*` indices. Round-trips through Driver.Restore (which
// substitutes `{target_db}` back to the destination worktree's
// prefix at load time).
func runESDump(ctx context.Context, conn *config.EsConn, prefix, outPath string, gzipOut bool) (*mcpsdk.CallToolResult, dbDumpOut, error) {
	if conn == nil {
		return nil, dbDumpOut{}, errors.New("connections.elasticsearch not configured")
	}
	drv, err := dbes.Connect(ctx, *conn)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	var w io.WriteCloser = f
	if gzipOut {
		w = gzip.NewWriter(f)
	}
	if err := drv.Dump(ctx, prefix, w); err != nil {
		if gzipOut {
			_ = w.Close()
		}
		_ = f.Close()
		return nil, dbDumpOut{}, fmt.Errorf("es dump: %w", err)
	}
	// Flush+close on the success path with the error checked: a
	// failed gzip footer or final file write would otherwise leave a
	// truncated dump that we'd report as a success.
	if gzipOut {
		if err := w.Close(); err != nil {
			return nil, dbDumpOut{}, fmt.Errorf("finalize gzip dump %s: %w", outPath, err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("finalize dump %s: %w", outPath, err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	return nil, dbDumpOut{Path: outPath, SizeBytes: info.Size()}, nil
}

func runMongoDump(
	ctx context.Context,
	conn *config.MongoConn,
	db, outPath string,
	gzipOut bool,
) (*mcpsdk.CallToolResult, dbDumpOut, error) {
	if _, err := exec.LookPath("mongodump"); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("mongodump not on PATH: %w", err)
	}
	cfgPath, removeCfg, err := dbmongo.ToolConfigFile(conn.URI)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	defer removeCfg()
	args := []string{
		"--config=" + cfgPath,
		"--db=" + db,
		"--archive=" + outPath,
		"--quiet",
	}
	if gzipOut {
		args = append(args, "--gzip")
	}
	cmd := exec.CommandContext(ctx, "mongodump", args...)
	if err := cmd.Run(); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("mongodump: %w", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	return nil, dbDumpOut{Path: outPath, SizeBytes: info.Size()}, nil
}

// runDumpCmd streams cmd.Stdout to outPath, optionally gzip-compressed
// in-process. Returns the final byte count.
func runDumpCmd(cmd *exec.Cmd, outPath string, gzipOut bool) (*mcpsdk.CallToolResult, dbDumpOut, error) {
	f, err := os.Create(outPath)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	var w io.WriteCloser = f
	if gzipOut {
		w = gzip.NewWriter(f)
	}
	var stderr bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if gzipOut {
			_ = w.Close()
		}
		_ = f.Close()
		return nil, dbDumpOut{}, fmt.Errorf("%s: %w: %s", cmd.Path, err, stderr.String())
	}
	// Flush+close on the success path with the error checked: a
	// failed gzip footer or final file write would otherwise leave a
	// truncated dump that we'd report as a success.
	if gzipOut {
		if err := w.Close(); err != nil {
			return nil, dbDumpOut{}, fmt.Errorf("finalize gzip dump %s: %w", outPath, err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, dbDumpOut{}, fmt.Errorf("finalize dump %s: %w", outPath, err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return nil, dbDumpOut{}, err
	}
	return nil, dbDumpOut{Path: outPath, SizeBytes: info.Size()}, nil
}

func coalescePort(v uint16, dflt int) int {
	if v == 0 {
		return dflt
	}
	return int(v)
}
