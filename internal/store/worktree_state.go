package store

// Status buckets a worktree's lifecycle state falls into, as shown by
// `treeman status`, the MCP status tool and the notification filter.
const (
	BucketStable = "stable"
	BucketUp     = "up"
	BucketDown   = "down"
	BucketFailed = "failed"
)

// Worktree lifecycle states derived by DeriveWorktreeState.
const (
	StateReady     = "ready"
	StatePreparing = "preparing"
	StateDeferred  = "deferred"
	StateError     = "error"
	StateTeardown  = "teardown"
)

// WorktreeStateEventTypes is the event set DeriveWorktreeState reads:
// the newest of these per worktree decides its state. Standalone
// prepare runs (MCP, plan tasks) emit only prepare:* terminal events,
// so prepare:end and prepare:error count too; otherwise a successful
// rerun would never clear an earlier failure.
var WorktreeStateEventTypes = []string{
	EvtWorktreeCreateStart, EvtWorktreeCreateEnd, EvtWorktreeCreateError,
	EvtWorktreeCreateDeferred,
	EvtWorktreeDeleteStart, EvtWorktreeDeleteEnd,
	EvtWorktreeReapStart, EvtWorktreeReapEnd,
	EvtPrepareEnd, EvtPrepareError,
}

// DeriveWorktreeState maps a worktree's newest WorktreeStateEventTypes
// event (the zero Event when it has none) to its state and bucket.
func DeriveWorktreeState(last Event) (state, bucket string) {
	switch last.EventType {
	case EvtWorktreeCreateStart:
		return StatePreparing, BucketUp
	case EvtWorktreeCreateDeferred:
		return StateDeferred, BucketStable
	case EvtWorktreeCreateError, EvtPrepareError:
		if last.Level == LevelError {
			return StateError, BucketFailed
		}
	case EvtWorktreeDeleteStart, EvtWorktreeReapStart:
		return StateTeardown, BucketDown
	}
	return StateReady, BucketStable
}
