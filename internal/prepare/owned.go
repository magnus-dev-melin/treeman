package prepare

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/template"
)

// teardownTarget renders the namespace a worktree owns for d: the
// database name (name_template) for name-scoped engines, the key /
// index / bucket prefix (key_prefix) for prefix-scoped ones, and the
// absolute file path for the file family. An empty result is an
// error: dropping "" would reach every namespace on the server.
func teardownTarget(
	ctx context.Context,
	cfg *config.Config,
	st *store.Store,
	worktreeID int64,
	d config.DatabaseConfig,
	fam engine.Family,
	tplCtx template.Context,
) (string, error) {
	tmpl := d.NameTemplate
	field := "name_template"
	if fam.Scope() == engine.ScopePrefix {
		tmpl, field = d.KeyPrefix, "key_prefix"
	}
	target, err := template.Render(tmpl, tplCtx)
	if err != nil {
		return "", fmt.Errorf("%s: render %s %q: %w", fam, field, tmpl, err)
	}
	if target == "" {
		return "", fmt.Errorf("%s: %s %q renders empty; refusing to drop every namespace", fam, field, tmpl)
	}
	if fam == engine.FamilyFile {
		return fileTeardownTarget(ctx, cfg, st, worktreeID, d.Connection, target)
	}
	return target, nil
}

// dropOwned drops what a worktree owns under target and returns the
// count dropped. Prefix-scoped engines own their whole prefix, and the
// file family owns the file plus its sidecars and clones, so both reap
// through DropMatching. Name-scoped server engines drop exact names
// only: target itself plus every live database matching the
// test-clone pattern. A prefix drop there would also take any sibling
// database whose name merely starts with target, which is every
// linked worktree when the main-worktree overlay binds a bare name.
func dropOwned(
	ctx context.Context,
	conn engineconn.Conn,
	fam engine.Family,
	target string,
	clones *config.TestClonesSpec,
	tplCtx template.Context,
) (int, error) {
	if fam.Scope() == engine.ScopePrefix || fam == engine.FamilyFile {
		return conn.DropMatching(ctx, target)
	}
	dropper, ok := conn.(engineconn.DatabaseDropper)
	if !ok {
		return 0, fmt.Errorf("%s: connection cannot drop a single database", fam)
	}
	names, err := ownedDatabases(ctx, conn, target, clones, tplCtx)
	if err != nil {
		return 0, err
	}
	var errs []error
	dropped := 0
	for _, n := range names {
		if err := dropper.DropDatabase(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: drop %s: %w", fam, n, err))
			continue
		}
		dropped++
	}
	return dropped, errors.Join(errs...)
}

// ownedDatabases lists target plus every live database named like one
// of its test clones.
func ownedDatabases(
	ctx context.Context,
	conn engineconn.Conn,
	target string,
	clones *config.TestClonesSpec,
	tplCtx template.Context,
) ([]string, error) {
	names := []string{target}
	prefix, suffix, ok, err := clonePattern(clones, tplCtx)
	if err != nil || !ok {
		return names, err
	}
	live, err := conn.ListMatching(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list test clones under %q: %w", prefix, err)
	}
	for _, n := range live {
		if n != target && isCloneName(n, prefix, suffix) {
			names = append(names, n)
		}
	}
	return names, nil
}

// clonePattern renders the parts of the test-clone name_template
// around its single `{n}`. ok is false when the database has no test
// clones.
func clonePattern(spec *config.TestClonesSpec, tplCtx template.Context) (prefix, suffix string, ok bool, err error) {
	if spec == nil || spec.NameTemplate == "" {
		return "", "", false, nil
	}
	before, after, found := strings.Cut(spec.NameTemplate, "{n}")
	if !found {
		return "", "", false, fmt.Errorf("test_clones.name_template %q has no {n}", spec.NameTemplate)
	}
	if prefix, err = template.Render(before, tplCtx); err != nil {
		return "", "", false, fmt.Errorf("render test_clones.name_template %q: %w", spec.NameTemplate, err)
	}
	if suffix, err = template.Render(after, tplCtx); err != nil {
		return "", "", false, fmt.Errorf("render test_clones.name_template %q: %w", spec.NameTemplate, err)
	}
	return prefix, suffix, true, nil
}

// isCloneName reports whether name is prefix + a decimal index + suffix.
func isCloneName(name, prefix, suffix string) bool {
	mid, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	if mid, ok = strings.CutSuffix(mid, suffix); !ok || mid == "" {
		return false
	}
	for i := range len(mid) {
		if mid[i] < '0' || mid[i] > '9' {
			return false
		}
	}
	return true
}
