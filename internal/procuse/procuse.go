package procuse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

type Use int

const (
	UseCwd Use = iota + 1
	UseExecutable
	UseMapped
	UseOpenFile
)

func (u Use) String() string {
	switch u {
	case UseCwd:
		return "working directory"
	case UseExecutable:
		return "executable"
	case UseMapped:
		return "mapped file"
	case UseOpenFile:
		return "open file"
	}
	return fmt.Sprintf("use(%d)", int(u))
}

type Holder struct {
	PID     int
	Command string
	Use     Use
}

func (h Holder) String() string {
	return fmt.Sprintf("pid %d %q (%s)", h.PID, h.Command, h.Use)
}

type Scan struct {
	Holders []Holder
	parents map[int]int
}

func Under(ctx context.Context, dir string) (Scan, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Scan{}, nil
	}
	if err != nil {
		return Scan{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	scan, err := scanProcesses(ctx, within(filepath.Clean(resolved)))
	if err != nil {
		return Scan{}, fmt.Errorf("scan processes using %s: %w", resolved, err)
	}
	return scan, nil
}

func (s Scan) Descends(pid, ancestor int) bool {
	for range len(s.parents) + 1 {
		if pid == ancestor {
			return true
		}
		parent, ok := s.parents[pid]
		if !ok || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

func within(root string) func(string) bool {
	prefix := root + string(filepath.Separator)
	if root == string(filepath.Separator) {
		prefix = root
	}
	return func(p string) bool {
		return p == root || strings.HasPrefix(p, prefix)
	}
}

func shorten(command string) string {
	const maxRunes = 160
	command = strings.TrimSpace(command)
	runes := []rune(command)
	if len(runes) <= maxRunes {
		return command
	}
	return string(runes[:maxRunes]) + "…"
}
