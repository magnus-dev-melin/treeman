package rpc

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/sunpath"
)

func TestSocketPathRejectsPathsOverTheSunPathLimit(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", sunpath.Max)
	for _, tc := range []struct {
		name, fix string
		env       map[string]string
	}{
		{"TREEMAN_SOCKET", "set " + SocketEnv, map[string]string{SocketEnv: long + "/tm.sock"}},
		{"XDG_RUNTIME_DIR", "point XDG_RUNTIME_DIR at a shorter directory", map[string]string{SocketEnv: "", "XDG_RUNTIME_DIR": long}},
		{"XDG_DATA_HOME", "set XDG_RUNTIME_DIR to a shorter directory", map[string]string{SocketEnv: "", "XDG_RUNTIME_DIR": "", "XDG_DATA_HOME": long}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path, err := SocketPath()
			var tooLong *sunpath.TooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("SocketPath() = %q, %v; want a sun_path TooLongError", path, err)
			}
			for _, want := range []string{tooLong.Path, strconv.Itoa(len(tooLong.Path)) + " bytes", strconv.Itoa(sunpath.Max), tc.fix} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if _, err := Call(
				context.Background(),
				Request{Method: MethodPing},
			); !errors.As(err, &tooLong) ||
				errors.Is(err, ErrDaemonUnreachable) {
				t.Errorf("Call with an over-long socket path = %v; want the sun_path error, not a daemon-unreachable fallback", err)
			}
		})
	}
}

func TestSocketPathAcceptsAPathAtTheLimit(t *testing.T) {
	p := "/" + strings.Repeat("s", sunpath.Max-1)
	t.Setenv(SocketEnv, p)
	if got, err := SocketPath(); err != nil || got != p {
		t.Fatalf("SocketPath() = %q, %v; want the %d-byte path accepted", got, err, len(p))
	}
}
