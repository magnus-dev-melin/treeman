//go:build e2e

package cli_surface_e2e

import (
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/sunpath"
)

func TestOverLongSocketPathFailsWithTheLimitAndTheFix(t *testing.T) {
	e := newEnv(t)
	sock := "/tmp/" + strings.Repeat("s", sunpath.Max)

	daemon := daemonCommand(t, e, sock)
	out, err := daemon.CombinedOutput()
	if err == nil {
		t.Fatalf("treemand bound an over-long socket path; output:\n%s", out)
	}
	for _, want := range []string{sock, "sun_path", "TREEMAN_SOCKET"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("treemand output does not name %q:\n%s", want, out)
		}
	}

	res := e.runEnv(t, e.home, []string{"TREEMAN_SOCKET=" + sock}, "daemon", "status")
	if res.err == nil || !strings.Contains(res.stdout+res.stderr, "sun_path") {
		t.Errorf("treeman daemon status with an over-long socket path: err=%v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
}
