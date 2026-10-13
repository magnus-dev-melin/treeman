//go:build darwin

package procuse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func scanProcesses(ctx context.Context, inside func(string) bool) (Scan, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-n", "-P", "-w", "-F", "pRcfn", "-u", strconv.Itoa(os.Getuid()))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(out) == 0) {
		return Scan{}, fmt.Errorf("lsof: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return parseLsof(out, inside)
}
