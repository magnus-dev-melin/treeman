package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestPatchFilterLeavesTheFileUnpatchedAndWarnsWhenTheRegistryIsUnreadable(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	gitT(t, "", "init", "-q", "-b", "main", repo)
	gitT(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, ".treeman.yaml"),
		[]byte("patches:\n  - file: .env\n    set:\n      DB_DATABASE: app_{slug}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".worktrees", "feat")
	gitT(t, repo, "worktree", "add", "-q", "-b", "feat", wt)

	dbPath := filepath.Join(t.TempDir(), "treeman.db")
	t.Setenv("TREEMAN_DB_PATH", dbPath)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, repo, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureWorktree(ctx, repoID, wt, "ticket-42", "feat"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "DROP TABLE worktree_ports"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)

	in := "DB_DATABASE=placeholder\n"
	var out bytes.Buffer
	var filterErr error
	_, warnings := captureOutErr(t, func() {
		filterErr = runPatchFilter(ctx, "smudge", ".env", strings.NewReader(in), &out)
	})
	if filterErr != nil {
		t.Fatal(filterErr)
	}
	if out.String() != in {
		t.Fatalf("smudge with an unreadable registry rendered %q; want the bytes unchanged instead of a guessed slug", out.String())
	}
	if !strings.Contains(warnings, ".env") || !strings.Contains(warnings, "unpatched") {
		t.Fatalf("no warning naming the file and the outcome on stderr: %q", warnings)
	}
}
