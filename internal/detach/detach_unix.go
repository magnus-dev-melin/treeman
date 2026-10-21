//go:build unix

package detach

import (
	"os/exec"
	"syscall"
)

func NewSession(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
