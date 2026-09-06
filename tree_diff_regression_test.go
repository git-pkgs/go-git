package git_test

import (
	"os/exec"
	"strings"
	"testing"

	git6 "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	object6 "github.com/go-git/go-git/v6/plumbing/object"
)

func TestDiffSkipsUnchangedMissingSubtree(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--quiet")
	a := git("before\n", "hash-object", "-w", "--stdin")
	b := git("after\n", "hash-object", "-w", "--stdin")
	missing := "040000 tree 1111111111111111111111111111111111111111\ta-missing\n"
	from := git(missing+"100644 blob "+a+"\tchanged\n", "mktree", "--missing")
	to := git(missing+"100644 blob "+b+"\tchanged\n", "mktree", "--missing")
	if out := git("", "diff-tree", "--no-commit-id", "--name-only", "-r", from, to); out != "changed" {
		t.Fatalf("native Git diff = %q", out)
	}
	r, err := git6.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	oldTree, err := r.TreeObject(plumbing.NewHash(from))
	if err != nil {
		t.Fatal(err)
	}
	newTree, err := r.TreeObject(plumbing.NewHash(to))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := object6.DiffTree(oldTree, newTree)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].To.Name != "changed" {
		t.Fatalf("got changes %v, want changed", changes)
	}
}
