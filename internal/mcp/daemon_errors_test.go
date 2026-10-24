package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/sunpath"
	"github.com/stubbedev/treeman/pkg/rpc"
)

func TestDaemonProbesReportNonDialFailuresInsteadOfNotRunning(t *testing.T) {
	t.Setenv(rpc.SocketEnv, "/tmp/"+strings.Repeat("s", sunpath.Max))
	ctx := context.Background()

	if _, out, err := daemonStatusTool(ctx, nil, daemonStatusIn{}); err == nil || !strings.Contains(err.Error(), "sun_path") {
		t.Errorf("daemon_status = %+v, %v; want the sun_path error, not not-running", out, err)
	}
	if got := collectDaemonState(ctx); got.Status != "error" || !strings.Contains(got.Error, "sun_path") {
		t.Errorf("daemon state = %+v; want status error carrying the sun_path error", got)
	}
	if got := checkDaemon(ctx); got.Status != "fail" || !strings.Contains(got.Detail, "sun_path") {
		t.Errorf("doctor daemon check = %+v; want fail with the sun_path error", got)
	}
	resp := submitMCPPlan(ctx, rpc.Plan(true, rpc.One(rpc.Task{Type: rpc.TaskWorktreeRegister, RepoPath: t.TempDir()})))
	if resp.Kind != rpc.KindError || !strings.Contains(resp.Message, "sun_path") {
		t.Errorf("plan submission = %+v; want a KindError with the sun_path error, not an in-process run", resp)
	}
}
