//go:build unix

package hooks

import (
	"os/exec"

	"github.com/stubbedev/treeman/internal/detach"
)

// shellCommand builds the POSIX-shell command that runs a hook on
// every unix platform treeman supports (linux, the BSDs, macOS):
// `/bin/sh -c` in its own session (Setsid) so a hook outlives the
// daemon restarting behind it. Windows has neither — see
// shell_other.go (#89).
func shellCommand(cmdStr string) (*exec.Cmd, error) {
	c := exec.Command("/bin/sh", "-c", cmdStr) //nolint:noctx // detached setsid hook; must outlive caller ctx
	if err := detach.NewSession(c); err != nil {
		return nil, err
	}
	return c, nil
}
