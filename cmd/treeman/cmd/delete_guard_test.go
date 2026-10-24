package cmd

import (
	"context"
	"strings"
	"testing"
)

func TestDeleteGuardFailsWhenGitCannotInspectTheWorktree(t *testing.T) {
	dir := t.TempDir()
	if reasons, err := unregenerableState(context.Background(), dir); err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("unregenerableState(non-repo) = %v, %v; want an error naming the path instead of \"nothing to lose\"", reasons, err)
	}
	if m := worktreeMarkers(context.Background(), dir); m != " ?" {
		t.Fatalf("worktreeMarkers(non-repo) = %q; want \" ?\" so an unreadable worktree is not shown as clean", m)
	}
}
