package snapshot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/store"
)

// countingDropConn is a fake engineconn.Conn: DropSnapshot always
// succeeds, dials are counted at the connect-func level.
type countingDropConn struct {
	mu      *sync.Mutex
	dropped *[]string
}

func (c countingDropConn) Close() error                                  { return nil }
func (c countingDropConn) EngineVersion(context.Context) (string, error) { return "", nil }
func (c countingDropConn) Exists(context.Context, string) (bool, error)  { return false, nil }
func (c countingDropConn) DropSnapshot(_ context.Context, n string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.dropped = append(*c.dropped, n)
	return nil
}

func (c countingDropConn) DropMatching(_ context.Context, n string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.dropped = append(*c.dropped, n)
	return 1, nil
}

func (c countingDropConn) ListMatching(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c countingDropConn) SizeKB(context.Context, string) int64 { return 0 }

// TestEvictCandidatesOneDialPerFamily pins the #55 acceptance
// criterion: evicting N templates performs at most #engines connects
// (counted at the conn layer), every template still drops, and the
// rows + events land for exactly the dropped set.
func TestEvictCandidatesOneDialPerFamily(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}

	var dials atomic.Int64
	dropped := &[]string{}
	fake := func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error) {
		dials.Add(1)
		return countingDropConn{mu: &sync.Mutex{}, dropped: dropped}, true, nil
	}

	n := 12
	cands := make([]store.SnapshotEvictionCandidate, 0, n)
	for i := range n {
		fp := fingerprintForTest(i)
		cands = append(cands, store.SnapshotEvictionCandidate{
			Fingerprint: fp, Engine: "mysql", TemplateName: "_tm_t" + string(rune('a'+i)), SourceDB: "src",
		})
	}
	evictCandidates(ctx, newDropPool(&config.Config{}, fake), st, cands, repoID, store.EvtSnapshotsEvictCap, "test", testEvictMsg)

	if got := dials.Load(); got != 1 {
		t.Errorf("dials = %d, want 1 (one conn for the whole mysql batch)", got)
	}
	// Every template + its spare family drops: 2 entries per candidate.
	if len(*dropped) != 2*n {
		t.Errorf("drops = %d, want %d", len(*dropped), 2*n)
	}
	rows, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows survived eviction, want 0", len(rows))
	}
}

// TestEvictCandidatesSkipsPinned pins that a pinned fingerprint is
// neither dropped nor has its row deleted.
func TestEvictCandidatesSkipsPinned(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}

	dropped := &[]string{}
	fake := func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error) {
		return countingDropConn{mu: &sync.Mutex{}, dropped: dropped}, true, nil
	}

	pinned := fingerprintForTest(1)
	free := fingerprintForTest(2)
	if err := st.RecordSnapshot(ctx, store.SnapshotRecord{
		Fingerprint: pinned, RepoID: repoID, Engine: "mysql", TemplateName: "_tm_p", SourceDB: "s",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSnapshot(ctx, store.SnapshotRecord{
		Fingerprint: free, RepoID: repoID, Engine: "mysql", TemplateName: "_tm_f", SourceDB: "s",
	}); err != nil {
		t.Fatal(err)
	}
	unpin := Pin(pinned)
	defer unpin()

	evictCandidates(ctx, newDropPool(&config.Config{}, fake), st, []store.SnapshotEvictionCandidate{
		{Fingerprint: pinned, Engine: "mysql", TemplateName: "_tm_p"},
		{Fingerprint: free, Engine: "mysql", TemplateName: "_tm_f"},
	}, repoID, store.EvtSnapshotsEvictCap, "test", testEvictMsg)

	for _, name := range *dropped {
		if name == "_tm_p" || name == "_tm_p"+PrewarmSuffix {
			t.Errorf("pinned template %q was dropped", name)
		}
	}
	rows, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Fingerprint != pinned {
		t.Errorf("pinned row must survive: %+v", rows)
	}
}

func testEvictMsg(store.SnapshotEvictionCandidate) string { return "test" }

// cancelAfterDropConn drops successfully, then cancels the eviction's
// context — the moment a closing store or an expiring deadline lands
// between a template's drop and its row delete.
type cancelAfterDropConn struct {
	countingDropConn
	cancel context.CancelFunc
}

func (c cancelAfterDropConn) DropSnapshot(ctx context.Context, n string) error {
	err := c.countingDropConn.DropSnapshot(ctx, n)
	c.cancel()
	return err
}

// TestEvictCandidatesDeletesRowsOfDroppedTemplatesAfterCancel pins
// #131: once a template is dropped, its snapshot row goes too, even if
// the eviction's context is cancelled right after the drop. Otherwise
// `snapshots list` keeps rows for templates that no longer exist.
func TestEvictCandidatesDeletesRowsOfDroppedTemplatesAfterCancel(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(context.Background(), "/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	fp := fingerprintForTest(7)
	if err := st.RecordSnapshot(context.Background(), store.SnapshotRecord{
		Fingerprint: fp, RepoID: repoID, Engine: "mysql", TemplateName: "_tm_c", SourceDB: "s",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dropped := &[]string{}
	fake := func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error) {
		return cancelAfterDropConn{mu: &sync.Mutex{}, dropped: dropped, cancel: cancel}, true, nil
	}
	evictCandidates(ctx, newDropPool(&config.Config{}, fake), st, []store.SnapshotEvictionCandidate{
		{Fingerprint: fp, Engine: "mysql", TemplateName: "_tm_c", SourceDB: "s"},
	}, repoID, store.EvtSnapshotsEvictCap, "test", testEvictMsg)

	if len(*dropped) == 0 {
		t.Fatal("template was not dropped")
	}
	rows, err := st.ListSnapshotsForRepo(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("row of a dropped template survived the cancelled eviction: %+v", rows)
	}
}

// fingerprintForTest derives a stable 64-hex fingerprint from an int,
// shaped like Key.Fingerprint output.
func fingerprintForTest(i int) string {
	k := Key{FormatVersion: 5, Engine: "mysql", HashMode: "migrations", MigrationsHashHex: fingerprintHex(i)}
	return k.Fingerprint()
}

func fingerprintHex(i int) string {
	const hexDigits = "0123456789abcdef"
	s := make([]byte, 64)
	for j := range s {
		s[j] = hexDigits[(i+j)%16]
	}
	return string(s)
}
