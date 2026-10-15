package daemon

import (
	"testing"
)

func rerunIndexes(rs []WatchRerun) []int {
	out := make([]int, len(rs))
	for i, r := range rs {
		out[i] = r.DBIndex
	}
	return out
}

func TestWatchRerunQueuesOnlyBehindInFlightFinalize(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()

	if !st.MarkFinalizeInFlightOrQueue("/wt", nil, WatchRerun{DBIndex: 0}) {
		t.Fatal("free slot: want the watch run to claim it")
	}
	if got := st.UnmarkFinalizeInFlight("/wt"); len(got) != 0 {
		t.Fatalf("claiming run queued itself: %+v", got)
	}
}

func TestWatchRerunCoalescesPerDatabase(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()

	if !st.MarkFinalizeInFlight("/wt", nil) {
		t.Fatal("mark in-flight")
	}
	for _, r := range []WatchRerun{
		{DBIndex: 1, RunID: "a"},
		{DBIndex: 0, RunID: "b"},
		{DBIndex: 1, RunID: "c"},
	} {
		if st.MarkFinalizeInFlightOrQueue("/wt", nil, r) {
			t.Fatalf("busy slot claimed by %+v", r)
		}
	}
	got := st.UnmarkFinalizeInFlight("/wt")
	if idx := rerunIndexes(got); len(idx) != 2 || idx[0] != 0 || idx[1] != 1 {
		t.Fatalf("queued db indexes = %v, want [0 1]", idx)
	}
	if got[1].RunID != "c" {
		t.Errorf("db 1 rerun = %+v, want the latest event (c)", got[1])
	}
	if st.IsFinalizeInFlight("/wt") {
		t.Error("unmark left the slot in flight")
	}
}

func TestWatchRerunAllDatabasesAbsorbsPerDatabase(t *testing.T) {
	st, cleanup := setup(t)
	defer cleanup()

	if !st.MarkFinalizeInFlight("/wt", nil) {
		t.Fatal("mark in-flight")
	}
	for _, idx := range []int{0, -1, 2} {
		st.MarkFinalizeInFlightOrQueue("/wt", nil, WatchRerun{DBIndex: idx})
	}
	if idx := rerunIndexes(st.UnmarkFinalizeInFlight("/wt")); len(idx) != 1 || idx[0] != -1 {
		t.Fatalf("queued db indexes = %v, want [-1]", idx)
	}
}
