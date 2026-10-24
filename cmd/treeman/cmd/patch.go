package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/fspath"
	"github.com/stubbedev/treeman/internal/gitcmd"
	"github.com/stubbedev/treeman/internal/patcher"
	"github.com/stubbedev/treeman/internal/resolve"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/template"
	"github.com/stubbedev/treeman/internal/ui"
)

// PatchFilterCmd backs git's clean/smudge filter for `patches:`.
// Invoked by git via `filter.treeman-patch.{clean,smudge}` for each
// patched file on every status/diff/add/checkout/merge. Reads the
// file content from stdin, writes the transformed bytes to stdout.
//
// Mode arg is positional and matches git's terminology:
//
//	clean  : working tree → index. Project patched keys back to
//	         HEAD's value so git sees the file as unchanged for
//	         those keys (lets pull / merge overwrite without
//	         tripping "would be overwritten" refusals).
//	smudge : index → working tree. Apply patches.<set> with the
//	         current worktree's template context (slug, ports).
//
// Hidden from `--help`: this is a git internal, not a user command.
// Failures fall through with stdin → stdout so a misconfigured
// filter degrades to a no-op rather than corrupting working trees.
func PatchFilterCmd() *cli.Command {
	return &cli.Command{
		Name:      "patch-filter",
		Hidden:    true,
		Usage:     "git clean/smudge filter for patches: — invoked by git, not by users",
		ArgsUsage: "<clean|smudge> <relative-path>",
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() < 2 {
				return errors.New("patch-filter: usage: treeman patch-filter <clean|smudge> <relative-path>")
			}
			mode := c.Args().Get(0)
			relPath := c.Args().Get(1)
			if mode != "clean" && mode != "smudge" {
				return fmt.Errorf("patch-filter: mode must be clean or smudge (got %q)", mode)
			}
			return runPatchFilter(ctx, mode, relPath, os.Stdin, os.Stdout)
		},
	}
}

// runPatchFilter is the testable core. Reads input from `in`, writes
// to `out`. Resolves the worktree from cwd, finds the matching
// patches[] entry, dispatches Smudge or Clean.
//
// Pass-through semantics: no matching patch entry and the main
// worktree output stdin unchanged. A failure to work out what to
// render (cwd, repo root, config, registry) also passes the bytes
// through, since a filter that exits non-zero breaks the git command,
// but it warns on stderr, which git shows, so an unpatched file is
// never a silent outcome.
func runPatchFilter(ctx context.Context, mode, relPath string, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	unchanged := func(what string, err error) error {
		ui.Warn("patch-filter %s %s: %s: %v; leaving the file unpatched", mode, relPath, what, err)
		return passthrough(out, body)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return unchanged("resolve the worktree", err)
	}
	worktreePath := fspath.Canonical(cwd)
	repoRoot, err := DiscoverRepoRoot(worktreePath)
	if err != nil {
		return unchanged("resolve the repo of "+worktreePath, err)
	}

	cfg, err := resolve.LoadResolvedForWorktree(repoRoot, worktreePath)
	if err != nil {
		return unchanged("load the treeman config of "+worktreePath, err)
	}
	patch, ok := findPatchEntry(cfg.Patches, relPath)
	if !ok {
		return passthrough(out, body)
	}

	// Main worktree: patches don't apply. The filter still fires
	// because git reads `info/attributes` from $GIT_COMMON_DIR
	// (shared with every linked worktree), so the attribute lines
	// installed for a linked wt match the same paths in the main
	// checkout too. Hand back the bytes verbatim.
	if isMainWorktreePath(repoRoot, worktreePath) {
		return passthrough(out, body)
	}

	tplCtx, err := buildTemplateContext(ctx, repoRoot, worktreePath)
	if err != nil {
		return unchanged("read the template context", err)
	}

	switch mode {
	case "smudge":
		result, err := patcher.Smudge(patch, string(body), tplCtx)
		if err != nil {
			return passthrough(out, body)
		}
		_, err = out.Write([]byte(result))
		return err
	case "clean":
		headContent, _ := gitcmd.String(ctx, worktreePath, "show", "HEAD:"+relPath)
		result, err := patcher.Clean(patch, string(body), headContent)
		if err != nil {
			return passthrough(out, body)
		}
		_, err = out.Write([]byte(result))
		return err
	}
	return passthrough(out, body)
}

func passthrough(out io.Writer, body []byte) error {
	_, err := out.Write(body)
	return err
}

// findPatchEntry returns the patches[] entry whose File matches
// `relPath`. Match is by-path equality after cleanup — both sides
// normalised so `./.env` and `.env` resolve the same.
func findPatchEntry(patches []config.Patch, relPath string) (config.Patch, bool) {
	want := filepath.Clean(relPath)
	for _, p := range patches {
		if filepath.Clean(p.File) == want {
			return p, true
		}
	}
	return config.Patch{}, false
}

// isMainWorktreePath reports whether `worktreePath` is the repo's
// primary checkout. Treeman skips patches there entirely (the
// canonical `.env` belongs to the developer), so the filter must
// also no-op when invoked from the main worktree.
func isMainWorktreePath(repoRoot, worktreePath string) bool {
	return fspath.Canonical(repoRoot) == fspath.Canonical(worktreePath)
}

func buildTemplateContext(ctx context.Context, repoRoot, worktreePath string) (template.Context, error) {
	pathSlug := template.FromSlug(slug.For(worktreePath, ""))
	st, err := openDefaultStore(ctx)
	if err != nil {
		return template.Context{}, err
	}
	defer func() { _ = st.Close() }()

	repoID, err := st.LookupRepoID(ctx, repoRoot)
	if err != nil {
		return template.Context{}, fmt.Errorf("look up repo %s: %w", repoRoot, err)
	}
	if repoID == 0 {
		return pathSlug, nil
	}
	row, err := st.LookupActiveWorktreeByPath(ctx, worktreePath)
	if err != nil {
		return template.Context{}, fmt.Errorf("look up the worktree at %s: %w", worktreePath, err)
	}
	if row.ID == 0 {
		return pathSlug, nil
	}
	ports, err := st.LoadWorktreePorts(ctx, row.ID)
	if err != nil {
		return template.Context{}, fmt.Errorf("read the ports of worktree %s: %w", worktreePath, err)
	}
	return template.FromSlug(slug.Slug{Value: row.Slug, Source: slug.SourceTicket}).WithPorts(ports), nil
}
