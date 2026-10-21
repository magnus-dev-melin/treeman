//go:build unix

package detach

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestNewSessionLeavesCallerProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := NewSession(cmd); err != nil {
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
		t.Error("process started with NewSession is still in the caller's process group")
	}
}

func TestNewSessionHasNoControllingTerminal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "if (exec 3</dev/tty) 2>/dev/null; then echo has-tty; else echo no-tty; fi")
	if err := NewSession(cmd); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "no-tty\n" {
		t.Errorf("a new session must not be able to open /dev/tty, got %q", out)
	}
}
