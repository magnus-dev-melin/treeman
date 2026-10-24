package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorktreeOverHTTPWithoutARootFailsInsteadOfUsingTheServerCwd(t *testing.T) {
	ctx := withResolver(context.Background(), &reqResolver{httpMode: true})
	for _, arg := range []string{"", "feature-x"} {
		if wt, _, err := resolveWorktree(ctx, arg); err == nil {
			t.Errorf("resolveWorktree(%q) over HTTP with no workspace root = %q, nil; want an error", arg, wt)
		}
	}
}

func TestResolveWorktreeJoinsARelativePathToTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := withResolver(context.Background(), &reqResolver{httpMode: true, headerRoots: []string{root}})
	wt, _, err := resolveWorktree(ctx, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if want := canonicalDir(filepath.Join(root, "sub")); wt != want {
		t.Fatalf("resolveWorktree(\"sub\") = %q, want %q under the request's root, not the server cwd", wt, want)
	}
}

func TestSlugComputeAcceptsAnUnregisteredNameButNotAResolveFailure(t *testing.T) {
	root := t.TempDir()
	ctx := withResolver(context.Background(), &reqResolver{httpMode: true, headerRoots: []string{root}})
	_, out, err := slugComputeTool(ctx, nil, slugComputeIn{Path: "not-created-yet"})
	if err != nil || out.Slug == "" {
		t.Fatalf("slug_compute for an unregistered name = %+v, %v; want a computed slug", out, err)
	}
	var noMatch *noWorktreeMatchError
	if _, _, err := resolveWorktree(ctx, "not-created-yet"); !errors.As(err, &noMatch) {
		t.Fatalf("resolveWorktree(unregistered) = %v; want a noWorktreeMatchError", err)
	}
	httpNoRoot := withResolver(context.Background(), &reqResolver{httpMode: true})
	if _, out, err := slugComputeTool(httpNoRoot, nil, slugComputeIn{}); err == nil {
		t.Fatalf("slug_compute with no workspace root = %+v, nil; want the resolve error", out)
	}
}
