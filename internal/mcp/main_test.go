package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/testsock"
	"github.com/stubbedev/treeman/pkg/rpc"
)

// TestMain points the store, the daemon socket and the XDG dirs at a
// throwaway directory, so no tool under test can write to the
// developer's real event log or talk to their running daemon (#174).
// Daemon autostart is stubbed out the same way: failing `systemctl`
// and `treemand` shims shadow the host's, so a dispatch can neither
// start the installed systemd unit nor fork a stray treemand bound to
// the test socket. Tests that need a store of their own still set
// TREEMAN_DB_PATH.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	dir, err := testsock.NewDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated test dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create stub bin dir: %v\n", err)
		return 1
	}
	stub := []byte("#!/bin/sh\nexit 1\n")
	for _, name := range []string{"systemctl", "treemand"} {
		if err := os.WriteFile(filepath.Join(bin, name), stub, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "write %s stub: %v\n", name, err)
			return 1
		}
	}
	env := map[string]string{
		"PATH":            bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TREEMAN_DB_PATH": filepath.Join(dir, "treeman.db"),
		rpc.SocketEnv:     filepath.Join(dir, "treeman.sock"),
		"XDG_DATA_HOME":   filepath.Join(dir, "data"),
		"XDG_STATE_HOME":  filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_RUNTIME_DIR": filepath.Join(dir, "run"),
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "set %s for test isolation: %v\n", k, err)
			return 1
		}
	}
	return m.Run()
}
