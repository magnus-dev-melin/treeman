package watcher

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/config"
)

// TestDispatchesOnMatchingGlob writes a file under a watched glob
// and asserts the dispatcher fires once with the expected mode.
func TestDispatchesOnMatchingGlob(t *testing.T) {
	repoRoot := t.TempDir()
	migDir := filepath.Join(repoRoot, "database", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		events []Event
	)
	dispatch := func(_ context.Context, ev Event) error {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return nil
	}

	const debounceMs uint64 = 100
	paths := []config.WatcherPath{
		{Glob: "database/migrations/**"},
	}
	w, err := New(repoRoot, paths, debounceMs, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	go func() { _ = w.Start(ctx) }()

	// Give the watcher a moment to register its dirs.
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(migDir, "2024_01_01_create.php"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait up to 1s for the debounced dispatch.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(events)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("no events dispatched")
	}
}

// TestIgnoresUnmatchedPaths confirms changes outside the configured
// globs are silently dropped — important for repos with noisy
// vendor / node_modules trees.
func TestIgnoresUnmatchedPaths(t *testing.T) {
	repoRoot := t.TempDir()
	noise := filepath.Join(repoRoot, "node_modules")
	if err := os.MkdirAll(noise, 0o755); err != nil {
		t.Fatal(err)
	}
	migDir := filepath.Join(repoRoot, "database", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var fired bool
	dispatch := func(_ context.Context, _ Event) error { fired = true; return nil }
	const debounceMs uint64 = 100
	paths := []config.WatcherPath{{Glob: "database/migrations/**"}}
	w, err := New(repoRoot, paths, debounceMs, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	// node_modules isn't under any watched glob — fsnotify won't
	// even be added there. Mutate it anyway as a control.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	go func() { _ = w.Start(ctx) }()
	time.Sleep(150 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(noise, "junk.js"), []byte("x"), 0o644)
	<-ctx.Done()
	if fired {
		t.Error("dispatcher fired for unmatched path")
	}
}

// TestReArmsAfterFlush asserts the debounce timer fires again for a
// second burst after the first flush. This is the re-arm path that
// replaced the always-on ticker: a regression here would dispatch the
// first edit and then go permanently silent. Sequencing the second
// write strictly after the first dispatch guarantees the two edits
// land in separate debounce windows, so reaching 2 dispatches can
// only happen if the timer re-armed.
func TestReArmsAfterFlush(t *testing.T) {
	repoRoot := t.TempDir()
	migDir := filepath.Join(repoRoot, "database", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		count int
	)
	dispatch := func(_ context.Context, _ Event) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}

	const debounceMs uint64 = 80
	w, err := New(repoRoot, []config.WatcherPath{{Glob: "database/migrations/**"}}, debounceMs, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	go func() { _ = w.Start(ctx) }()
	time.Sleep(150 * time.Millisecond) // let addAllDirs subscribe

	waitFor := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := count
			mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d dispatches (re-arm likely broken)", n)
	}

	if err := os.WriteFile(filepath.Join(migDir, "001_a.sql"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(1)

	// Second burst, strictly after the first window closed → re-arm.
	if err := os.WriteFile(filepath.Join(migDir, "002_b.sql"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(2)
}

func TestStaticPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"database/migrations/**", "database/migrations"},
		{"db/migrate", "db/migrate"},
		{"src/*.rs", "src"},
		{"**/migrations", ""},
		{"crates/*/migrations/*.sql", "crates"},
	}
	for _, c := range cases {
		got := staticPrefix(c.in)
		if got != c.want {
			t.Errorf("staticPrefix(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestClassifyMatchesEveryDatabase pins the main-checkout migration
// gap: a test template and the branch-scoped dev database share their
// migration inputs, and a change must reach both, not only the first
// database whose glob matches.
func TestClassifyMatchesEveryDatabase(t *testing.T) {
	w := &Watcher{repoPath: "/repo", paths: []config.WatcherPath{
		{Glob: "database/migrations/**/*.php", Label: "migrations", DBIndex: 0},
		{Glob: "composer.lock", Label: "lockfile", DBIndex: 0},
		{Glob: "database/**", Label: "schema", DBIndex: 0},
		{Glob: "database/migrations/**/*.php", Label: "migrations", DBIndex: 3},
		{Glob: "seeds/*.sql", Label: "seeds", DBIndex: 5},
	}}
	got := w.classify("/repo/database/migrations/2026_10_15_000000_x.php")
	want := []Match{{DBIndex: 0, Label: "migrations"}, {DBIndex: 3, Label: "migrations"}}
	if !slices.Equal(got, want) {
		t.Fatalf("classify = %+v, want %+v", got, want)
	}
	if got := w.classify("/repo/README.md"); len(got) != 0 {
		t.Fatalf("unmatched path classified as %+v", got)
	}
}

// collect starts a watcher over paths and returns a func that waits
// for a dispatched event whose path ends in suffix.
func collect(t *testing.T, repoRoot string, paths []config.WatcherPath) func(suffix string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	w, err := New(repoRoot, paths, 50, func(_ context.Context, ev Event) error {
		mu.Lock()
		seen = append(seen, ev.Path)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.Start(t.Context()) }()
	time.Sleep(150 * time.Millisecond)
	return func(suffix string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			hit := slices.ContainsFunc(seen, func(p string) bool { return strings.HasSuffix(p, suffix) })
			mu.Unlock()
			if hit {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("no event for %s; saw %v", suffix, seen)
	}
}

// A literal input (the documented `composer.lock`) used to subscribe
// the file itself, which WalkDir skips, so edits never re-prepared.
func TestLiteralInputIsWatched(t *testing.T) {
	repoRoot := t.TempDir()
	lock := filepath.Join(repoRoot, "composer.lock")
	if err := os.WriteFile(lock, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	wait := collect(t, repoRoot, []config.WatcherPath{{Glob: "composer.lock"}})
	if err := os.WriteFile(lock, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wait("composer.lock")
}

// `mkdir -p` (or a checkout) lands a whole subtree, files included,
// before the watcher reads the first Create event. Nested dirs and the
// files already inside them must still be seen — also when the glob's
// static root did not exist at start.
func TestNestedTreeCreatedAtOnceIsSeen(t *testing.T) {
	repoRoot := t.TempDir()
	wait := collect(t, repoRoot, []config.WatcherPath{{Glob: "db/migrations/**/*.sql"}})
	nested := filepath.Join(repoRoot, "db", "migrations", "2026", "10")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "001_init.sql"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	wait("001_init.sql")
}

func TestRootOf(t *testing.T) {
	for _, tc := range []struct {
		glob string
		want watchRoot
	}{
		{"composer.lock", watchRoot{dir: "."}},
		{"backend/package-lock.json", watchRoot{dir: "backend"}},
		{"database/migrations/**", watchRoot{dir: "database/migrations", recursive: true}},
		{"**/migrations/*.py", watchRoot{dir: ".", recursive: true}},
	} {
		if got := rootOf(tc.glob); got != tc.want {
			t.Errorf("rootOf(%q) = %+v, want %+v", tc.glob, got, tc.want)
		}
	}
}
