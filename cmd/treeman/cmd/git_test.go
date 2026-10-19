package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/gitx"
)

func newGitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		full := append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "f"), "base")
	run("add", ".")
	run("commit", "-qm", "init")
	return repo, run
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWipeChangesKeepsExistingStashWhenNothingIsStashed(t *testing.T) {
	repo, run := newGitRepo(t)
	writeFile(t, filepath.Join(repo, "f"), "keep me")
	run("stash", "push", "-m", "precious")

	wiped, err := wipeChanges(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if wiped {
		t.Error("wiped = true on a clean tree")
	}
	if got := run("stash", "list"); !strings.Contains(got, "precious") {
		t.Fatalf("pre-existing stash was dropped; stash list = %q", got)
	}
}

func TestWipeChangesDiscardsWorkAndLeavesStashStack(t *testing.T) {
	repo, run := newGitRepo(t)
	writeFile(t, filepath.Join(repo, "f"), "keep me")
	run("stash", "push", "-m", "precious")
	writeFile(t, filepath.Join(repo, "f"), "throw away")
	writeFile(t, filepath.Join(repo, "untracked"), "throw away")

	wiped, err := wipeChanges(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !wiped {
		t.Error("wiped = false on a dirty tree")
	}
	if got := run("status", "--porcelain"); got != "" {
		t.Errorf("tree still dirty: %q", got)
	}
	if got := run("stash", "list"); strings.Count(got, "\n") != 0 || !strings.Contains(got, "precious") {
		t.Fatalf("stash stack changed; stash list = %q", got)
	}
}

// With one modified file the porcelain's leading space was trimmed off
// and the row lost ("Nothing to stage"); a path with a space came back
// C-quoted and could not be staged or discarded.
func TestStageRowsSeesEveryChange(t *testing.T) {
	repo, _ := newGitRepo(t)
	writeFile(t, filepath.Join(repo, "f"), "modified")
	writeFile(t, filepath.Join(repo, "a b.txt"), "new")

	rows, err := stageRows(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []gitx.StageRow{
		{Kind: gitx.StageModified, Path: "f"},
		{Kind: gitx.StageAdded, Path: "a b.txt"},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("stageRows = %+v, want %+v", rows, want)
	}
}

// currentBranch used to read <dir>/.git/HEAD, so from any subdirectory
// it saw no branch: push reported a detached HEAD and commit lost its
// ticket prefix.
func TestCurrentBranchFromSubdirectory(t *testing.T) {
	repo, run := newGitRepo(t)
	run("switch", "-q", "-c", "feature/KON-7-x")
	sub := filepath.Join(repo, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, dir := range []string{repo, sub} {
		if got, err := currentBranch(ctx, dir); err != nil || got != "feature/KON-7-x" {
			t.Errorf("currentBranch(%s) = %q, %v", dir, got, err)
		}
	}
	run("switch", "-q", "--detach")
	if got, err := currentBranch(ctx, sub); err != nil || got != "" {
		t.Errorf("detached: currentBranch = %q, %v; want \"\", nil", got, err)
	}
	if _, err := currentBranch(ctx, t.TempDir()); err == nil {
		t.Error("outside a repo: want an error")
	}
}
