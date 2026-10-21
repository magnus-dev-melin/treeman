//go:build !unix

package detach

import (
	"fmt"
	"os/exec"
	"runtime"
)

func NewSession(*exec.Cmd) error {
	return fmt.Errorf("starting a process in its own session is not supported on %s", runtime.GOOS)
}
