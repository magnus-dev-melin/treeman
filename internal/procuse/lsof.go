package procuse

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
)

func parseLsof(out []byte, inside func(string) bool) (Scan, error) {
	scan := Scan{parents: map[int]int{}}
	var (
		pid, ppid int
		command   string
		fd        string
		held      Use
	)
	flush := func() {
		if pid == 0 {
			return
		}
		scan.parents[pid] = ppid
		if held != 0 {
			scan.Holders = append(scan.Holders, Holder{PID: pid, Command: shorten(command), Use: held})
		}
	}
	lines := bufio.NewScanner(bytes.NewReader(out))
	for lines.Scan() {
		line := lines.Text()
		if line == "" {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			flush()
			n, err := strconv.Atoi(value)
			if err != nil {
				return Scan{}, fmt.Errorf("parse lsof pid %q: %w", value, err)
			}
			pid, ppid, command, fd, held = n, 0, "", "", 0
		case 'R':
			n, err := strconv.Atoi(value)
			if err != nil {
				return Scan{}, fmt.Errorf("parse lsof parent pid %q: %w", value, err)
			}
			ppid = n
		case 'c':
			command = value
		case 'f':
			fd = value
		case 'n':
			if held == 0 && fd != "rtd" && inside(value) {
				held = lsofUse(fd)
			}
		}
	}
	if err := lines.Err(); err != nil {
		return Scan{}, fmt.Errorf("read lsof output: %w", err)
	}
	flush()
	return scan, nil
}

func lsofUse(fd string) Use {
	switch fd {
	case "cwd":
		return UseCwd
	case "txt":
		return UseMapped
	}
	return UseOpenFile
}
