package fspath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalResolvesSymlinksEvenBelowAMissingLeaf(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := Canonical(link); got != resolvedReal {
		t.Errorf("Canonical(existing link) = %q, want %q", got, resolvedReal)
	}
	gone := filepath.Join(link, "worktrees", "gone")
	if got, want := Canonical(gone), filepath.Join(resolvedReal, "worktrees", "gone"); got != want {
		t.Errorf("Canonical(missing path under a link) = %q, want %q", got, want)
	}
	if got := Canonical("relative/../x"); got != "x" {
		t.Errorf("Canonical(relative) = %q, want the cleaned path", got)
	}
}
