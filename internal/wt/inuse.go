package wt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/stubbedev/treeman/internal/procuse"
	"github.com/stubbedev/treeman/pkg/rpc"
)

type InUseError struct {
	Path    string
	Holders []procuse.Holder
}

func (e *InUseError) Error() string {
	holders := make([]string, len(e.Holders))
	for i, h := range e.Holders {
		holders[i] = h.String()
	}
	return fmt.Sprintf("worktree %s is in use by %s; stop those processes or move them out of it, then retry",
		e.Path, strings.Join(holders, ", "))
}

func EnsureNotInUse(ctx context.Context, wtPath string) error {
	scan, err := procuse.Under(ctx, wtPath)
	if err != nil {
		return fmt.Errorf("check whether worktree %s is in use: %w", wtPath, err)
	}
	daemon, err := daemonPID(ctx)
	if err != nil {
		return fmt.Errorf("check whether worktree %s is in use: %w", wtPath, err)
	}
	holders := foreignHolders(scan, os.Getpid(), daemon)
	if len(holders) == 0 {
		return nil
	}
	return &InUseError{Path: wtPath, Holders: holders}
}

func foreignHolders(scan procuse.Scan, self, daemon int) []procuse.Holder {
	var foreign []procuse.Holder
	for _, h := range scan.Holders {
		if scan.Descends(self, h.PID) || (daemon > 0 && scan.Descends(h.PID, daemon)) {
			continue
		}
		foreign = append(foreign, h)
	}
	return foreign
}

func daemonPID(ctx context.Context) (int, error) {
	resp, err := rpc.Call(ctx, rpc.Request{Method: rpc.MethodStatus})
	if errors.Is(err, rpc.ErrDaemonUnreachable) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ask the daemon for its pid: %w", err)
	}
	return int(resp.Pid), nil
}
