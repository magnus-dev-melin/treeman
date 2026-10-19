//go:build unix

package daemonctl

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon in its own session, out of the caller's
// process group: a Ctrl-C in the terminal that autostarted it must not
// SIGINT the daemon mid-finalize or mid-teardown.
func detach(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
