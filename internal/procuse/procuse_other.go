//go:build !linux && !darwin

package procuse

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

func scanProcesses(context.Context, func(string) bool) (Scan, error) {
	return Scan{}, fmt.Errorf("process scan on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
