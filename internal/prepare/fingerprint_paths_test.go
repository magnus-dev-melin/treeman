package prepare

import (
	"context"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
)

// Two inputs sharing a basename (frontend/ and backend/
// package-lock.json) used to fold under one key: one overwrote the
// other, which one won depended on map order, and a change to the
// loser never invalidated the template.
func TestFingerprintSeesEverySameNamedInput(t *testing.T) {
	const wt = "/wt"
	key := func(front, back string) string {
		walks := []inputWalk{{glob: "**/package-lock.json", hashByAbs: map[string]string{
			wt + "/frontend/package-lock.json": front,
			wt + "/backend/package-lock.json":  back,
		}}}
		return keyFromWalks(context.Background(), nil, walks, config.DatabaseConfig{Engine: "mysql"}, wt, "8.4").Fingerprint()
	}
	base := key("f1", "b1")
	if key("f2", "b1") == base || key("f1", "b2") == base {
		t.Fatal("changing one of two same-named inputs left the fingerprint unchanged")
	}
	for range 20 {
		if key("f1", "b1") != base {
			t.Fatal("fingerprint is not deterministic")
		}
	}
	walks := []inputWalk{{glob: "**/package-lock.json", hashByAbs: map[string]string{
		"/other/frontend/package-lock.json": "f1",
		"/other/backend/package-lock.json":  "b1",
	}}}
	if keyFromWalks(context.Background(), nil, walks, config.DatabaseConfig{Engine: "mysql"}, "/other", "8.4").Fingerprint() != base {
		t.Error("same inputs in another worktree must share the fingerprint")
	}
}
