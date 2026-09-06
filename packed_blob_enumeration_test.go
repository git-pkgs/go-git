package git_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestPackedBlobPayloadErrorIsDeferredToRead(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable required")
	}
	path := t.TempDir()
	command := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	command("", "init", "--quiet")
	content := strings.Repeat("// SPDX-License-Identifier: MIT\n", 1000)
	hash := command(content, "hash-object", "-w", "--stdin")
	packHash := command(hash+"\n", "pack-objects", ".git/objects/pack/pack")
	command("", "prune-packed")
	packPath := filepath.Join(path, ".git", "objects", "pack", "pack-"+packHash+".pack")
	data, err := os.ReadFile(packPath)
	require.NoError(t, err)
	// Corrupt the zlib checksum, leaving the object header intact.
	data[len(data)-21] ^= 1
	require.NoError(t, os.Chmod(packPath, 0o600))
	require.NoError(t, os.WriteFile(packPath, data, 0o600))
	require.Equal(t, "blob", command(hash+"\n", "cat-file", "--batch-check=%(objecttype)"))
	cmd := exec.Command("git", "-C", path, "cat-file", "blob", hash)
	_, err = cmd.Output()
	require.Error(t, err)

	r, err := git.PlainOpen(path)
	require.NoError(t, err)
	defer r.Close()
	iter, err := r.Storer.IterEncodedObjects(plumbing.BlobObject)
	require.NoError(t, err)
	defer iter.Close()
	o, err := iter.Next()
	require.NoError(t, err)
	require.Equal(t, hash, o.Hash().String())
	require.Equal(t, int64(len(content)), o.Size())
	rd, err := o.Reader()
	require.NoError(t, err)
	defer rd.Close()
	_, err = io.ReadAll(rd)
	require.Error(t, err)
}
