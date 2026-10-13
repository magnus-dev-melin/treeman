package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/pkg/rpc"
)

func TestRunPlanCarriesRequesterToTasks(t *testing.T) {
	st := newTestState(t)
	seen := make(chan map[string]any, 1)
	register(t, "__t_trigger", func(ctx context.Context, _ *State, _ rpc.Task) (json.RawMessage, error) {
		seen <- triggerPayload(ctx)
		return nil, nil
	})
	requester := &rpc.Requester{PID: 4242, PPID: 4241, Command: "treeman worktree back --remove --force"}
	req := rpc.Plan(true, rpc.One(rpc.Task{Type: "__t_trigger"}))
	req.RunPlan.Requester = requester

	if resp := handleRunPlan(context.Background(), st, req); resp.Kind != rpc.KindPlanResult {
		t.Fatalf("kind: %s", resp.Kind)
	}
	want := map[string]any{"trigger": trigger{Source: triggerRPC, RequestedBy: requester}}
	if got := <-seen; !reflect.DeepEqual(got, want) {
		t.Fatalf("task trigger payload = %#v, want %#v", got, want)
	}
}

func TestQueuedPlanStartEventNamesRequester(t *testing.T) {
	st := newTestState(t)
	register(t, "__t_trigger_queued", func(context.Context, *State, rpc.Task) (json.RawMessage, error) {
		return nil, nil
	})
	req := rpc.Plan(false, rpc.One(rpc.Task{Type: "__t_trigger_queued"}))
	req.RunPlan.Requester = &rpc.Requester{PID: 7, PPID: 6, Command: "treeman worktree delete wt"}

	handleRunPlan(context.Background(), st, req)
	waitForEvent(t, st, store.EvtPlanEnd)

	evs, err := st.Store.QueryEvents(context.Background(),
		store.EventFilter{EventTypes: []string{store.EvtPlanStart}, Limit: 1})
	if err != nil || len(evs) != 1 {
		t.Fatalf("plan:start events = %v, err %v", evs, err)
	}
	var payload struct {
		Trigger trigger `json:"trigger"`
	}
	if err := json.Unmarshal([]byte(evs[0].PayloadJSON), &payload); err != nil {
		t.Fatalf("decode plan:start payload %q: %v", evs[0].PayloadJSON, err)
	}
	if payload.Trigger.Source != triggerRPC || payload.Trigger.RequestedBy == nil ||
		payload.Trigger.RequestedBy.Command != "treeman worktree delete wt" {
		t.Fatalf("plan:start trigger = %+v", payload.Trigger)
	}
}

func TestInheritTrigger(t *testing.T) {
	src := withTrigger(context.Background(), trigger{Source: triggerHeadChange})
	if got := triggerPayload(inheritTrigger(context.Background(), src)); got["trigger"] != (trigger{Source: triggerHeadChange}) {
		t.Fatalf("inherited payload = %#v", got)
	}
	if got := triggerPayload(inheritTrigger(context.Background(), context.Background())); got != nil {
		t.Fatalf("no trigger to inherit must yield no payload, got %#v", got)
	}
}
