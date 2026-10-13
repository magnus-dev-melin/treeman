package procuse

import (
	"reflect"
	"testing"
)

func TestParseLsof(t *testing.T) {
	out := []byte("p100\nR1\nczsh\nfcwd\nn/repo/.worktrees/wt\nftxt\nn/bin/zsh\n" +
		"p200\nR100\ncphp\nfcwd\nn/home/me\nftxt\nn/repo/.worktrees/wt/vendor/lib.dylib\n" +
		"p300\nR100\ncnode\nfcwd\nn/home/me\nf12\nn/repo/.worktrees/wt/package.json\n" +
		"p400\nR1\ncvim\nfrtd\nn/repo/.worktrees/wt\nfcwd\nn/repo/.worktrees/wt-other\n")

	scan, err := parseLsof(out, within("/repo/.worktrees/wt"))
	if err != nil {
		t.Fatalf("parseLsof: %v", err)
	}
	want := []Holder{
		{PID: 100, Command: "zsh", Use: UseCwd},
		{PID: 200, Command: "php", Use: UseMapped},
		{PID: 300, Command: "node", Use: UseOpenFile},
	}
	if !reflect.DeepEqual(scan.Holders, want) {
		t.Fatalf("holders = %v, want %v", scan.Holders, want)
	}
	if !scan.Descends(300, 1) || scan.Descends(400, 100) {
		t.Fatalf("parent chain wrong: %v", scan.parents)
	}
}

func TestParseLsofRejectsMalformedPid(t *testing.T) {
	if _, err := parseLsof([]byte("pabc\n"), within("/x")); err == nil {
		t.Fatal("malformed pid line must fail")
	}
}
