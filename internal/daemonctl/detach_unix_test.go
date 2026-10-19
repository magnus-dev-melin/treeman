//go:build unix

package daemonctl

import (
	"os/exec"
	"syscall"
	"testing"
)

// An autostarted daemon used to share the CLI's process group, so a
// Ctrl-C in that terminal SIGINTed the daemon too.
func TestDetachLeavesCallerProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := detach(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid == syscall.Getpgrp() {
		t.Error("detached process is still in the caller's process group")
	}
}
