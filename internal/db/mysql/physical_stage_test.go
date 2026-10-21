package mysql

import (
	"strings"
	"testing"
)

func TestStageDirsAreUniquePerStageUnderTheTemplateRoot(t *testing.T) {
	root := templateStageRoot("/var/lib/mysql-files/", "_tm_abc")
	if root != "/var/lib/mysql-files/_tm_stage/_tm_abc" {
		t.Fatalf("template stage root = %q", root)
	}
	first := stageDirFor("/var/lib/mysql-files/", "_tm_abc", newStageID())
	second := stageDirFor("/var/lib/mysql-files/", "_tm_abc", newStageID())
	if first == second {
		t.Fatalf("two stages of one template share %q; concurrent drivers would clobber each other", first)
	}
	for _, dir := range []string{first, second} {
		if !strings.HasPrefix(dir, root+"/") {
			t.Errorf("stage dir %q is not under the template root %q that DropSnapshot removes", dir, root)
		}
	}
}
