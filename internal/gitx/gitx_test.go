package gitx

import (
	"slices"
	"testing"
)

func TestTicketPrefix(t *testing.T) {
	cases := map[string]string{
		"feature/KON-1234-foo": "KON-1234",
		"bugfix/ABC-7":         "ABC-7",
		"master":               "",
		"release/1.2.3":        "",
		"KON-1234":             "KON-1234",
	}
	for in, want := range cases {
		if got := TicketPrefix(in); got != want {
			t.Errorf("TicketPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyTicketPrefix(t *testing.T) {
	cases := []struct{ msg, prefix, want string }{
		{"fix login", "KON-1234", "KON-1234: fix login"},
		{"fix login", "", "fix login"},
		{"ABC-9: already", "KON-1234", "ABC-9: already"}, // explicit prefix wins
		{"KON-1234: same", "KON-1234", "KON-1234: same"},
	}
	for _, c := range cases {
		if got := ApplyTicketPrefix(c.msg, c.prefix); got != c.want {
			t.Errorf("ApplyTicketPrefix(%q,%q) = %q, want %q", c.msg, c.prefix, got, c.want)
		}
	}
}

func TestProtectedPush(t *testing.T) {
	if ProtectedPush("feature/foo", nil) != "" {
		t.Error("feature/foo should be unprotected")
	}
	if ProtectedPush("master", nil) == "" {
		t.Error("master should be protected")
	}
	if ProtectedPush("release/1.2.3", nil) == "" {
		t.Error("release/1.2.3 should match release/*")
	}
	if ProtectedPush("hotfix/x", nil) == "" {
		t.Error("hotfix/x should match hotfix/*")
	}
	// zsh string-glob semantics: `*` crosses `/`, so nested release
	// paths are protected too — matching the old shell behavior the
	// repo `gp.protected` patterns were written against.
	if ProtectedPush("release/a/b", nil) == "" {
		t.Error("release/a/b should match release/* (zsh glob semantics)")
	}
	if ProtectedPush("qa", []string{"qa"}) == "" {
		t.Error("extra glob 'qa' should protect qa")
	}
}

func TestHandleize(t *testing.T) {
	cases := map[string]string{
		"KON 1234 Fix the login": "KON-1234-fix-the-login",
		"kon-1234-fix":           "KON-1234-fix",
		"KON1234":                "KON-1234",
		"add2fa":                 "add2fa", // digit run not ending on sep → not a key
		"Fix The Thing":          "fix-the-thing",
		"  spaced  out  ":        "spaced-out",
		"feature//weird__name":   "feature-weird-name",
	}
	for in, want := range cases {
		if got := Handleize(in); got != want {
			t.Errorf("Handleize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPatchFilename(t *testing.T) {
	got := PatchFilename("feature/KON-1", "remotes/origin/develop")
	want := "feature-KON-1--develop.diff"
	if got != want {
		t.Errorf("PatchFilename = %q, want %q", got, want)
	}
}

func TestStageRows(t *testing.T) {
	// The first entry's leading space is significant: trimming the
	// output used to turn " M tracked.go" into an unparsable line.
	porcelain := " M tracked.go\x00?? new file.txt\x00?? subdir/\x00 D gone.go\x00" +
		"R  renamed.go\x00old.go\x00RM moved.go\x00was.go\x00M  staged-only.go\x00"
	want := []StageRow{
		{Kind: StageModified, Path: "tracked.go"},
		{Kind: StageAdded, Path: "new file.txt"},
		{Kind: StageAdded, Path: "subdir/", ExpandDir: true},
		{Kind: StageDeleted, Path: "gone.go"},
		{Kind: StageModified, Path: "moved.go"},
	}
	if got := StageRows(porcelain); !slices.Equal(got, want) {
		t.Errorf("StageRows = %+v\nwant %+v", got, want)
	}
}
