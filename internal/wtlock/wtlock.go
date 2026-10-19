// Package wtlock serialises per-worktree work ACROSS processes.
//
// The daemon's per-worktree guards (MarkFinalizeInFlight,
// withWorktreePrepare) are in-process mutexes, but prepare.Run is also
// reached from other processes: `treeman worktree finalize --local`
// and the MCP server both run it inline. Issue #123: a `finalize
// --local` issued while the daemon's detached create tail was still
// building the same worktree dropped and dump-loaded the testing DB
// under the daemon's feet; the daemon's migrate died half-way and the
// test DB was left on a stale schema.
//
// Locks are advisory files under `<state dir>/locks/`, keyed by kind +
// a hash of the worktree path, so two worktrees never contend.
//
// The same directory holds the store-wide daemon lock (AcquireDaemon):
// at most one treemand serves a given store at a time.
package wtlock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/treeman/internal/store"
)

// Lock kinds. A caller holding Finalize may take Prepare inside it;
// never the other way round.
const (
	// Finalize covers a whole setup + prepare tail (hooks included).
	Finalize = "finalize"
	// Prepare covers one prepare.Run / RunFiltered call.
	Prepare = "prepare"
)

// pollInterval is how often a blocked Acquire retries.
var pollInterval = 200 * time.Millisecond

// errBusy is what the platform tryLock returns when another holder has
// the lock.
var errBusy = errors.New("lock busy")

// ErrDaemonRunning is returned by AcquireDaemon when another treemand
// already serves the store.
var ErrDaemonRunning = errors.New("another treemand is already serving this store")

func lockDir() (string, error) {
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dbPath), "locks"), nil
}

// Path returns the lock file for (kind, wtPath).
func Path(kind, wtPath string) (string, error) {
	dir, err := lockDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(wtPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(dir, kind+"-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

func openLockFile(p string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
}

// AcquireDaemon takes the store-wide daemon lock without waiting and
// records the caller's pid in it. It fails with ErrDaemonRunning while
// another process holds it. The returned func releases the lock.
func AcquireDaemon() (func(), error) {
	dir, err := lockDir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "treemand.lock")
	f, err := openLockFile(p)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock %s: %w", p, err)
	}
	if err := tryLock(f); err != nil {
		holder, readErr := io.ReadAll(f)
		_ = f.Close()
		if !errors.Is(err, errBusy) {
			return nil, fmt.Errorf("lock %s: %w", p, err)
		}
		if readErr != nil || len(holder) == 0 {
			return nil, fmt.Errorf("%w (lock %s)", ErrDaemonRunning, p)
		}
		return nil, fmt.Errorf("%w (pid %s, lock %s)", ErrDaemonRunning, strings.TrimSpace(string(holder)), p)
	}
	release := func() {
		_ = unlock(f)
		_ = f.Close()
	}
	if err := f.Truncate(0); err != nil {
		release()
		return nil, fmt.Errorf("record pid in %s: %w", p, err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		release()
		return nil, fmt.Errorf("record pid in %s: %w", p, err)
	}
	return release, nil
}

// Acquire takes the exclusive (kind, wtPath) lock, blocking until it is
// free or ctx is done. onWait (optional) runs once, the first time the
// lock is found held, so callers can tell the user why they are paused.
// The returned func releases the lock.
//
// The lock is tied to an open file, so a holder that crashes releases
// it with its file descriptors — no stale-lock recovery is needed.
func Acquire(ctx context.Context, kind, wtPath string, onWait func()) (func(), error) {
	p, err := Path(kind, wtPath)
	if err != nil {
		return nil, err
	}
	f, err := openLockFile(p)
	if err != nil {
		return nil, err
	}
	waited := false
	for {
		err := tryLock(f)
		if err == nil {
			return func() {
				_ = unlock(f)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, errBusy) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", p, err)
		}
		if !waited && onWait != nil {
			onWait()
		}
		waited = true
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
