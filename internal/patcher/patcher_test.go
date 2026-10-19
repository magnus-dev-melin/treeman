package patcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpfile(t *testing.T, content string) string {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "f")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvReplaceExistingKey(t *testing.T) {
	p := tmpfile(t, "FOO=old\nBAR=keep\n")
	r, err := PatchEnvFile(p, []Pair{{"FOO", "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if r != Updated {
		t.Errorf("want Updated, got %v", r)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "FOO=new\nBAR=keep\n" {
		t.Errorf("got %q", string(b))
	}
}

func TestEnvAppendMissingKey(t *testing.T) {
	p := tmpfile(t, "FOO=keep\n")
	r, err := PatchEnvFile(p, []Pair{{"BAR", "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if r != Updated {
		t.Errorf("want Updated, got %v", r)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "FOO=keep\nBAR=new\n" {
		t.Errorf("got %q", string(b))
	}
}

func TestEnvIdempotentWhenValueMatches(t *testing.T) {
	p := tmpfile(t, "FOO=already\n")
	r, _ := PatchEnvFile(p, []Pair{{"FOO", "already"}})
	if r != Unchanged {
		t.Errorf("want Unchanged, got %v", r)
	}
}

func TestPhpunitReplaceExistingEnv(t *testing.T) {
	p := tmpfile(t, "<phpunit>\n<php>\n\t<env name=\"FOO\" value=\"old\"/>\n\t</php>\n</phpunit>\n")
	if _, err := PatchPhpunitFile(p, []Pair{{"FOO", "new"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `<env name="FOO" value="new" force="true"/>`) {
		t.Errorf("missing replaced env: %s", string(b))
	}
}

func TestPhpunitInjectNewEnv(t *testing.T) {
	p := tmpfile(t, "<phpunit>\n<php>\n\t</php>\n</phpunit>\n")
	if _, err := PatchPhpunitFile(p, []Pair{{"FOO", "bar"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `<env name="FOO" value="bar" force="true"/>`) {
		t.Errorf("missing injected env: %s", string(b))
	}
}

// `[^/]*` could not match an element whose value holds a `/`, so every
// run appended another <env> for a URL-valued key.
func TestPatchPhpunitIsIdempotentForURLValues(t *testing.T) {
	content := "<phpunit>\n\t<php>\n\t\t<env name=\"APP_URL\" value=\"http://old.test\"/>\n\t</php>\n</phpunit>\n"
	for range 3 {
		content = patchPhpunitOne(content, "APP_URL", "http://wt.test/a")
	}
	if n := strings.Count(content, `name="APP_URL"`); n != 1 {
		t.Fatalf("APP_URL appears %d times, want 1:\n%s", n, content)
	}
	if !strings.Contains(content, `value="http://wt.test/a"`) {
		t.Fatalf("value not replaced:\n%s", content)
	}
	if got := extractPhpunit(content, []string{"APP_URL"})["APP_URL"]; got != "http://wt.test/a" {
		t.Errorf("extractPhpunit = %q", got)
	}
	if removed := removePhpunitContent(content, []string{"APP_URL"}); strings.Contains(removed, "APP_URL") {
		t.Errorf("removePhpunitContent left the element:\n%s", removed)
	}
}
