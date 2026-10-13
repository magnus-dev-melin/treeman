//go:build linux

package procuse

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const procRoot = "/proc"

func scanProcesses(ctx context.Context, inside func(string) bool) (Scan, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return Scan{}, fmt.Errorf("list %s: %w", procRoot, err)
	}
	scan := Scan{parents: make(map[int]int, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Scan{}, err
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		ppid, err := parentOf(dir)
		if inaccessible(err) {
			continue
		}
		if err != nil {
			return Scan{}, err
		}
		scan.parents[pid] = ppid
		use, err := useOf(dir, inside)
		if err != nil {
			return Scan{}, err
		}
		if use != 0 {
			scan.Holders = append(scan.Holders, Holder{PID: pid, Command: commandOf(dir), Use: use})
		}
	}
	return scan, nil
}

func inaccessible(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ESRCH)
}

func parentOf(dir string) (int, error) {
	path := filepath.Join(dir, "stat")
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	_, rest, found := bytes.CutLast(raw, []byte(")"))
	if !found {
		return 0, fmt.Errorf("parse %s: no command terminator in %q", path, raw)
	}
	fields := strings.Fields(string(rest))
	if len(fields) < 2 {
		return 0, fmt.Errorf("parse %s: no parent pid in %q", path, raw)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("parse parent pid in %s: %w", path, err)
	}
	return ppid, nil
}

func useOf(dir string, inside func(string) bool) (Use, error) {
	for _, link := range [...]struct {
		name string
		use  Use
	}{{"cwd", UseCwd}, {"exe", UseExecutable}} {
		target, err := os.Readlink(filepath.Join(dir, link.name))
		if inaccessible(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if inside(target) {
			return link.use, nil
		}
	}
	if found, err := anyOpenFile(dir, inside); err != nil || found {
		return UseOpenFile, err
	}
	if found, err := anyMappedFile(dir, inside); err != nil || found {
		return UseMapped, err
	}
	return 0, nil
}

func anyOpenFile(dir string, inside func(string) bool) (bool, error) {
	fdDir := filepath.Join(dir, "fd")
	fds, err := os.ReadDir(fdDir)
	if inaccessible(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("list %s: %w", fdDir, err)
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if inaccessible(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if inside(target) {
			return true, nil
		}
	}
	return false, nil
}

func anyMappedFile(dir string, inside func(string) bool) (bool, error) {
	path := filepath.Join(dir, "maps")
	f, err := os.Open(path)
	if inaccessible(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := lines.Text()
		if i := strings.IndexByte(line, '/'); i >= 0 && inside(line[i:]) {
			return true, nil
		}
	}
	if err := lines.Err(); err != nil && !inaccessible(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return false, nil
}

func commandOf(dir string) string {
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(raw) > 0 {
		return shorten(string(bytes.ReplaceAll(bytes.TrimRight(raw, "\x00"), []byte{0}, []byte{' '})))
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		return shorten(string(raw))
	}
	return ""
}
