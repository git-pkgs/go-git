package git_test

import (
	"io"
	"os/exec"
	"strings"
	"testing"

	git6 "github.com/go-git/go-git/v6"
)

func historyFixture(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	r := t.TempDir()
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", r}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--quiet", "-b", "main")
	git("", "config", "user.name", "Object Bench")
	git("", "config", "user.email", "objectbench@example.invalid")
	git("", "config", "commit.gpgsign", "false")
	return r, git
}

func allCommits(path string) (map[string]bool, error) {
	r, err := git6.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	it, err := r.Log(&git6.LogOptions{All: true})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	seen := make(map[string]bool)
	for {
		c, err := it.Next()
		if err == io.EOF {
			return seen, nil
		}
		if err != nil {
			return seen, err
		}
		seen[c.Hash.String()] = true
	}
}

func TestLogAllMergeSideBranch(t *testing.T) {
	t.Parallel()
	r, git := historyFixture(t)
	tree := git("", "mktree")
	a := git("", "commit-tree", tree, "-m", "root")
	b := git("", "commit-tree", tree, "-m", "main", "-p", a)
	c := git("", "commit-tree", tree, "-m", "side", "-p", a)
	m := git("", "commit-tree", tree, "-m", "merge", "-p", b, "-p", c)
	git("", "update-ref", "refs/heads/main", b)
	git("", "update-ref", "refs/heads/merged", m)
	want := strings.Fields(git("", "rev-list", "--all"))
	seen, err := allCommits(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range want {
		if !seen[h] {
			t.Errorf("missing reachable commit %s", h)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("got %d commits, want %d", len(seen), len(want))
	}
}

func TestLogAllAnnotatedTag(t *testing.T) {
	t.Parallel()
	r, git := historyFixture(t)
	tree := git("", "mktree")
	a := git("", "commit-tree", tree, "-m", "main")
	d := git("", "commit-tree", tree, "-m", "tag-only root")
	git("", "update-ref", "refs/heads/main", a)
	git("", "-c", "tag.gpgsign=false", "tag", "-a", "release", d, "-m", "release")
	want := strings.Fields(git("", "rev-list", "--all"))
	seen, err := allCommits(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range want {
		if !seen[h] {
			t.Errorf("missing tagged commit %s", h)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("got %d commits, want %d", len(seen), len(want))
	}
}

func TestLogAllMissingParent(t *testing.T) {
	t.Parallel()
	r, git := historyFixture(t)
	tree := git("", "mktree")
	content := "tree " + tree + "\nparent 1111111111111111111111111111111111111111\nauthor Test <test@example.invalid> 1 +0000\ncommitter Test <test@example.invalid> 1 +0000\n\nbroken parent\n"
	h := git(content, "hash-object", "-t", "commit", "-w", "--stdin")
	git("", "update-ref", "refs/heads/main", h)
	cmd := exec.Command("git", "-C", r, "rev-list", "--all")
	if err := cmd.Run(); err == nil {
		t.Fatal("native Git accepted missing parent")
	}
	if seen, err := allCommits(r); err == nil {
		t.Fatalf("silently returned %d commits for broken history", len(seen))
	}
}
