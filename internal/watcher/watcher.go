// Package watcher tails filesystem events under a repo's main
// checkout and dispatches `prepare` reruns for every active worktree
// of that repo whenever a migration directory, seed dump, or
// lockfile changes.
//
// One `Watcher` per repo. Globs from `watcher.paths` decide which
// events the watcher cares about; each match resolves to a
// dispatch mode (`auto` | `delta` | `rebuild`). Events are debounced
// per (worktree, mode) pair so a `git pull` that drops 40 migration
// files at once collapses into one prepare run per worktree.
package watcher

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"

	"github.com/stubbedev/treeman/internal/config"
)

// Event is one debounced filesystem event ready for dispatch.
// The cache-hit / cold-build decision is derived from the input
// fingerprint downstream — there's no per-event mode override.
type Event struct {
	RepoPath string
	Path     string
	// Matches names every database whose inputs cover Path, one entry
	// per database in config order; never empty. Two databases often
	// share their inputs (a test template and the branch-scoped dev
	// database both migrate from the same files), and each must
	// re-prepare.
	Matches []Match
}

// Match is one database whose inputs cover an event's path.
type Match struct {
	// DBIndex is the index into cfg.Databases.
	DBIndex int
	// Label is the optional label on the database's first matching
	// Input, referenced by `hooks.file-change[].match:`.
	Label string
}

// Dispatcher is the callback invoked once per debounced event. The
// daemon implements this with the prepare orchestrator.
type Dispatcher func(ctx context.Context, ev Event) error

// Watcher tails one repo. Call Start in a goroutine; Stop to cancel.
type Watcher struct {
	repoPath string
	paths    []config.WatcherPath
	dispatch Dispatcher
	debounce time.Duration

	fsw *fsnotify.Watcher

	// Pending events keyed by (path, mode) so duplicates inside the
	// debounce window collapse.
	mu      sync.Mutex
	pending map[string]Event
}

// New constructs a Watcher. `paths` is the aggregated list of
// WatcherPath entries (each one carries its DBIndex + optional
// Label) the daemon collected by walking every database's
// `inputs:` block. `debounceMs` is the coalesce window in ms (0 →
// 500 ms default).
func New(repoPath string, paths []config.WatcherPath, debounceMs uint64, dispatch Dispatcher) (*Watcher, error) {
	if dispatch == nil {
		return nil, errors.New("watcher: nil dispatcher")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify new: %w", err)
	}
	// Clamp before the int64 conversion so a pathological config value
	// can't wrap time.Duration. maxDebounceMs is the largest ms window
	// that still multiplies cleanly into an int64 nanosecond duration.
	const maxDebounceMs = uint64(math.MaxInt64 / int64(time.Millisecond))
	if debounceMs > maxDebounceMs {
		debounceMs = maxDebounceMs
	}
	debounce := time.Duration(debounceMs) * time.Millisecond
	if debounce == 0 {
		debounce = 500 * time.Millisecond
	}
	return &Watcher{
		repoPath: repoPath,
		paths:    paths,
		dispatch: dispatch,
		debounce: debounce,
		fsw:      fsw,
		pending:  map[string]Event{},
	}, nil
}

// Start runs the watch loop until ctx is cancelled or fsnotify
// errors fatally. Adds every existing dir under each glob's prefix
// to fsnotify on first call (fsnotify is non-recursive on Linux);
// new subdirectories are picked up via the Create event handler.
func (w *Watcher) Start(ctx context.Context) error {
	defer func() { _ = w.fsw.Close() }()

	if len(w.paths) == 0 {
		// No globs declared — nothing to watch. Sleep until ctx
		// cancels so the WatcherEntry tracking stays alive (caller
		// uses our presence to count active watchers).
		<-ctx.Done()
		return nil
	}

	w.addAllDirs()

	// Debounce timer, armed only on the first event of a burst and
	// disarmed after each flush. This coalesces a burst (e.g. a `git
	// pull` dropping 40 migration files) into one flush ~debounceMs
	// after the first event — the same bounded latency the previous
	// always-on ticker gave, minus a wakeup every debounceMs while the
	// repo sits idle (which, across many watched worktrees, was pure
	// scheduler churn). Start the timer disarmed.
	timer := time.NewTimer(w.debounce)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	armed := false

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			// A directory created where matches can live is subscribed
			// with everything already inside it: `mkdir -p` or a
			// checkout lands a whole subtree before its Create event
			// is read, and fsnotify (non-recursive on Linux) would
			// otherwise miss both the nested dirs and the files
			// written into them.
			queued := w.enqueue(ev.Name)
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					w.subscribe(ev.Name, func(file string) {
						queued = w.enqueue(file) || queued
					})
				}
			}
			if !queued {
				continue
			}
			// Arm the debounce window on the first event of a burst.
			// Subsequent events ride the same window (no reset) so a
			// steady stream can't starve the flush past debounceMs.
			// Resetting only when disarmed keeps the timer channel
			// drained, so Reset never races a pending tick.
			if !armed {
				timer.Reset(w.debounce)
				armed = true
			}

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			slog.Warn("fsnotify error", "repo", w.repoPath, "err", err)

		case <-timer.C:
			armed = false
			w.flush(ctx)
		}
	}
}

// Stop closes the underlying fsnotify.Watcher; Start unblocks.
func (w *Watcher) Stop() { _ = w.fsw.Close() }

// enqueue records path as pending when it matches an input, reporting
// whether it did.
func (w *Watcher) enqueue(path string) bool {
	matches := w.classify(path)
	if len(matches) == 0 {
		return false
	}
	w.mu.Lock()
	w.pending[path] = Event{RepoPath: w.repoPath, Path: path, Matches: matches}
	w.mu.Unlock()
	return true
}

// noiseDirNames is the set of directory basenames we refuse to
// recurse into during addAllDirs. fsnotify is non-recursive on Linux
// but the WalkDir below would still descend node_modules/ and
// vendor/ if a glob's static prefix happened to enclose them — and
// even one inotify watch on a deep node_modules generates wakeups
// every time webpack or composer touches it. Filtering at subscribe
// time means the kernel never delivers the events at all. storage/
// is treeman's own per-worktree dump dir (~3G) — a walk over it at
// every watcher start was pure cost.
var noiseDirNames = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	".idea":        {},
	".vscode":      {},
	"dist":         {},
	"build":        {},
	"target":       {},
	".next":        {},
	".turbo":       {},
	".cache":       {},
	".direnv":      {},
	"storage":      {},
	"var":          {},
	"tmp":          {},
	"coverage":     {},
	"venv":         {},
	".venv":        {},
}

// watchRoot is where one input glob's matches can appear: dir
// (repo-relative, "." for the repo root) and, for a glob with
// wildcards, every directory below it. A literal path (composer.lock)
// can only change inside its parent directory, so only that one
// directory is watched — walking it would subscribe the whole repo.
type watchRoot struct {
	dir       string
	recursive bool
}

func rootOf(glob string) watchRoot {
	if !strings.ContainsAny(glob, "*?[{") {
		return watchRoot{dir: filepath.Dir(filepath.Clean(glob))}
	}
	return watchRoot{dir: filepath.Clean(cmp.Or(staticPrefix(glob), ".")), recursive: true}
}

// relevant reports whether the repo-relative directory rel can hold a
// match, or lies on the way to a root that does not exist yet.
func (w *Watcher) relevant(rel string) bool {
	for _, p := range w.paths {
		r := rootOf(p.Glob)
		if rel == r.dir || isUnder(r.dir, rel) || (r.recursive && isUnder(rel, r.dir)) {
			return true
		}
	}
	return false
}

// isUnder reports whether path lies strictly inside dir (both
// repo-relative, cleaned).
func isUnder(path, dir string) bool {
	return dir == "." && path != "." || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// addAllDirs subscribes every input glob's watch root. A root that does
// not exist yet is covered by watching its nearest existing ancestor,
// whose Create event then subscribes the root once it appears.
func (w *Watcher) addAllDirs() {
	for _, p := range w.paths {
		dir := filepath.Join(w.repoPath, rootOf(p.Glob).dir)
		for {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir || !strings.HasPrefix(parent, w.repoPath) {
				break
			}
			dir = parent
		}
		w.subscribe(dir, nil)
	}
}

// subscribe adds dir and every relevant directory below it to fsnotify
// (fsnotify reports events for a directory's entries without
// recursing), skipping noise trees below dir. onFile, when set, sees
// every regular file found on the way.
func (w *Watcher) subscribe(dir string, onFile func(string)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries and keep walking
		}
		if !d.IsDir() {
			if onFile != nil {
				onFile(path)
			}
			return nil
		}
		rel, rerr := filepath.Rel(w.repoPath, path)
		if rerr != nil || !w.relevant(rel) {
			return filepath.SkipDir
		}
		// Skip noise dirs entirely — never subscribe inside them. The
		// subscribed dir itself is always added (a glob pointed at
		// `node_modules/**` means it).
		if _, noisy := noiseDirNames[d.Name()]; noisy && path != dir {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(path); err != nil {
			slog.Debug("fsnotify add skipped", "path", path, "err", err)
		}
		return nil
	})
}

// classify resolves an event path against the configured globs and
// returns one Match per database with a matching glob, in config
// order, labelled by that database's first matching glob. Unmatched
// paths drop on the floor (the user explicitly listed which paths
// matter via inputs:; everything else is noise).
func (w *Watcher) classify(path string) []Match {
	rel, err := filepath.Rel(w.repoPath, path)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	var matches []Match
	for _, p := range w.paths {
		if slices.ContainsFunc(matches, func(m Match) bool { return m.DBIndex == p.DBIndex }) {
			continue
		}
		if ok, _ := doublestar.PathMatch(p.Glob, rel); ok {
			matches = append(matches, Match{DBIndex: p.DBIndex, Label: p.Label})
		}
	}
	return matches
}

// flush dispatches every pending event then clears the buffer.
// Runs sequentially: dispatch is expected to be fast (it enqueues
// work to background goroutines, not blocking).
func (w *Watcher) flush(ctx context.Context) {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	events := slices.SortedFunc(maps.Values(w.pending), func(a, b Event) int { return strings.Compare(a.Path, b.Path) })
	w.pending = map[string]Event{}
	w.mu.Unlock()

	for _, e := range events {
		if err := w.dispatch(ctx, e); err != nil {
			slog.Warn("watcher dispatch", "path", e.Path, "err", err)
		}
	}
}

// staticPrefix returns the longest leading directory prefix of
// `glob` that contains no glob meta-characters.
func staticPrefix(glob string) string {
	for i, c := range glob {
		switch c {
		case '*', '?', '[', '{':
			// Trim back to the last separator before this char.
			cut := glob[:i]
			if idx := lastSep(cut); idx >= 0 {
				return cut[:idx]
			}
			return ""
		}
	}
	return glob
}

func lastSep(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == filepath.Separator {
			return i
		}
	}
	return -1
}
