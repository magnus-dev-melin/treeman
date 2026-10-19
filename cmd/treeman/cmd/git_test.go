package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
