//go:build !unix

package daemonctl

import (
	"fmt"
	"os/exec"
	"runtime"
)

// detach is the non-unix stub: there is no session to move the daemon
// into, so starting it would leave it in the caller's console group.
func detach(*exec.Cmd) error {
	return fmt.Errorf("starting treemand detached is not supported on %s", runtime.GOOS)
}
