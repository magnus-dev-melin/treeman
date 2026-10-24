package notify

import (
	"context"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

func TestBucketMapsLifecycleEvents(t *testing.T) {
	cases := []struct {
		eventType string
		level     string
		want      string
	}{
		{store.EvtWorktreeCreateStart, "info", store.BucketUp},
		{store.EvtWorktreeCreateEnd, "info", store.BucketStable},
		{store.EvtWorktreeCreateError, "error", store.BucketFailed},
		// wt_finalize at a non-error level is not a failure transition.
		{store.EvtWorktreeCreateError, "info", ""},
		{store.EvtWorktreeDeleteStart, "info", store.BucketDown},
		{store.EvtWorktreeReapStart, "info", store.BucketDown},
		// Terminal teardown completion is intentionally unmapped.
		{store.EvtWorktreeDeleteEnd, "info", ""},
		{store.EvtWorktreeReapEnd, "info", ""},
		// Unrelated events never notify.
		{"auto_fetch_done", "info", ""},
		{store.EvtConfigReload, "info", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := Bucket(c.eventType, c.level); got != c.want {
			t.Errorf("Bucket(%q, %q) = %q, want %q", c.eventType, c.level, got, c.want)
		}
	}
}

func TestComposeIncludesRepoAndTarget(t *testing.T) {
	n := Compose(store.BucketStable, "myrepo", "feature/x", "")
	if n.Title != "treeman: ready" {
		t.Errorf("title = %q", n.Title)
	}
	if want := "myrepo · feature/x finished preparing"; n.Body != want {
		t.Errorf("body = %q, want %q", n.Body, want)
	}
	if n.Urgency != UrgencyNormal {
		t.Errorf("urgency = %q, want normal", n.Urgency)
	}
}

func TestComposeFailedIsCritical(t *testing.T) {
	n := Compose(store.BucketFailed, "myrepo", "feature/x", "")
	if n.Urgency != UrgencyCritical {
		t.Errorf("failed urgency = %q, want critical", n.Urgency)
	}
	if want := "myrepo · feature/x failed to prepare — run: treeman worktree show feature/x"; n.Body != want {
		t.Errorf("failed body without detail = %q, want %q", n.Body, want)
	}
}

// TestComposeFailedSurfacesErrorAndFollowUp pins the failed-bucket
// body: the real error's first line plus a ready-to-paste follow-up
// command, with the whole thing capped so backends can't mangle it.
func TestComposeFailedSurfacesErrorAndFollowUp(t *testing.T) {
	n := Compose(store.BucketFailed, "myrepo", "feature/x", "migrate failed: unknown column\n  at line 12\nstack frame")
	want := "myrepo · feature/x failed to prepare: migrate failed: unknown column — run: treeman worktree show feature/x"
	if n.Body != want {
		t.Errorf("body = %q, want %q", n.Body, want)
	}

	// A huge error is ellipsized into the remaining budget while the
	// follow-up command survives intact at the end.
	n = Compose(store.BucketFailed, "myrepo", "feature/x", strings.Repeat("x", 500))
	if len([]rune(n.Body)) > maxBodyLen {
		t.Errorf("body is %d runes, want <= %d", len([]rune(n.Body)), maxBodyLen)
	}
	if !strings.HasSuffix(n.Body, "treeman worktree show feature/x") {
		t.Errorf("cap dropped the follow-up command: %q", n.Body)
	}
	if !strings.Contains(n.Body, "…") {
		t.Errorf("long error not ellipsized: %q", n.Body)
	}

	// Success/teardown banners stay unchanged by detail.
	if n := Compose(store.BucketStable, "myrepo", "feature/x", "boom"); strings.Contains(n.Body, "boom") {
		t.Errorf("stable banner leaked detail: %q", n.Body)
	}
}

func TestComposeOmitsEmptyTargetAndRepo(t *testing.T) {
	n := Compose(store.BucketUp, "myrepo", "", "")
	if want := "myrepo is being prepared"; n.Body != want {
		t.Errorf("body with empty target = %q, want %q", n.Body, want)
	}
	n = Compose(store.BucketUp, "", "", "")
	if want := "worktree is being prepared"; n.Body != want {
		t.Errorf("body with empty repo+target = %q, want %q", n.Body, want)
	}
}

func TestNewSenderNoneIsUnavailable(t *testing.T) {
	s := NewSender("none")
	if s.Available() {
		t.Error("none backend reported available")
	}
	if err := s.Send(context.Background(), Notification{Title: "t", Body: "b"}); err != nil {
		t.Errorf("none.Send returned error: %v", err)
	}
}

func TestFailedTeardownsNotifyAsFailed(t *testing.T) {
	for _, evt := range []string{store.EvtWorktreeDeleteError, store.EvtWorktreeReapError} {
		if got := Bucket(evt, store.LevelError); got != store.BucketFailed {
			t.Errorf("Bucket(%s, error) = %q, want failed", evt, got)
		}
		if got := Bucket(evt, store.LevelWarn); got != "" {
			t.Errorf("Bucket(%s, warn) = %q, want no notification", evt, got)
		}
		if state, _ := store.DeriveWorktreeState(store.Event{EventType: evt, Level: store.LevelError}); state != store.StateError {
			t.Errorf("state after %s = %q, want error", evt, state)
		}
	}
}
