package prepare

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/template"
)

// serverConn is a fake name-scoped server: a set of live databases
// with prefix listing and exact drops.
type serverConn struct {
	countingConn
	live map[string]bool
}

func (c serverConn) ListMatching(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for n := range c.live {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (c serverConn) DropDatabase(_ context.Context, n string) error {
	delete(c.live, n)
	return nil
}

func newServer(names ...string) serverConn {
	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
	}
	return serverConn{dropped: &[]string{}, live: live}
}

func (c serverConn) remaining() []string {
	out := make([]string, 0, len(c.live))
	for n := range c.live {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// The main-worktree overlay binds bare names (kontainer_testing). A
// prefix drop of that name used to take every linked worktree's
// kontainer_testing_<slug> source and clones with it.
func TestDropOwnedBareMainNameSparesSiblings(t *testing.T) {
	srv := newServer(
		"app_testing", "app_testing_test_1", "app_testing_test_2",
		"app_testing_kon_1_abcdef01", "app_testing_kon_1_abcdef01_test_1",
		"app_testing_backup",
	)
	clones := &config.TestClonesSpec{NameTemplate: "app_testing_test_{n}"}
	tplCtx := template.FromSlug(slug.ForMain("/repo", "main"))

	n, err := dropOwned(context.Background(), srv, engine.FamilyMySQL, "app_testing", clones, tplCtx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("dropped %d, want 3 (source + 2 clones)", n)
	}
	want := []string{"app_testing_backup", "app_testing_kon_1_abcdef01", "app_testing_kon_1_abcdef01_test_1"}
	if got := srv.remaining(); !slices.Equal(got, want) {
		t.Errorf("remaining = %v, want %v", got, want)
	}
}

func TestDropOwnedWorktreeDropsItsCloneFamily(t *testing.T) {
	sl := slug.Slug{Value: "kon_1_abcdef01"}
	tplCtx := template.FromSlug(sl)
	srv := newServer(
		"app_kon_1_abcdef01", "app_kon_1_abcdef01_test_1", "app_kon_1_abcdef01_test_12",
		"app_kon_1_abcdef01_test_x", "app_kon_12_abcdef02",
	)
	clones := &config.TestClonesSpec{NameTemplate: "app_{slug}_test_{n}"}

	if _, err := dropOwned(context.Background(), srv, engine.FamilyPostgres, "app_kon_1_abcdef01", clones, tplCtx); err != nil {
		t.Fatal(err)
	}
	want := []string{"app_kon_12_abcdef02", "app_kon_1_abcdef01_test_x"}
	if got := srv.remaining(); !slices.Equal(got, want) {
		t.Errorf("remaining = %v, want %v", got, want)
	}
}

func TestTeardownTargetRefusesEmptyName(t *testing.T) {
	d := config.DatabaseConfig{Engine: "elasticsearch", NameTemplate: "unused_{slug}"}
	_, err := teardownTarget(context.Background(), &config.Config{}, nil, 0, d, engine.FamilyES,
		template.FromSlug(slug.Slug{Value: "s_00000000"}))
	if err == nil || !strings.Contains(err.Error(), "renders empty") {
		t.Fatalf("empty key_prefix must be refused, got %v", err)
	}
}

func TestIsCloneName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"app_test_1", true},
		{"app_test_10_x", true},
		{"app_test_", false},
		{"app_test_1a_x", false},
		{"app_test__x", false},
	} {
		suffix := ""
		if strings.HasSuffix(tc.name, "_x") {
			suffix = "_x"
		}
		if got := isCloneName(tc.name, "app_test_", suffix); got != tc.want {
			t.Errorf("isCloneName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
