package git_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestPackedTypeFilteringMatchesNative(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			path := t.TempDir()
			run := func(input []byte, args ...string) []byte {
				cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
				cmd.Stdin = bytes.NewReader(input)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				return out
			}
			run(nil, "init", "--bare", "--object-format="+format)
			blob := []byte("SPDX-License-Identifier: MIT\n")
			blobHash := strings.TrimSpace(string(run(blob, "hash-object", "-w", "--stdin")))
			rawHash, err := hex.DecodeString(blobHash)
			require.NoError(t, err)
			want := map[string][]byte{blobHash: blob}
			hashes := make([]string, 1, 17)
			hashes[0] = blobHash
			for version := range 16 {
				var tree bytes.Buffer
				for n := range 1000 {
					name := fmt.Sprintf("100644 file-%04d", n)
					if n == 0 {
						name += fmt.Sprint(version)
					}
					tree.WriteString(name)
					tree.WriteByte(0)
					tree.Write(rawHash)
				}
				hash := strings.TrimSpace(string(run(tree.Bytes(), "hash-object", "-t", "tree", "-w", "--stdin")))
				want[hash] = tree.Bytes()
				hashes = append(hashes, hash)
			}
			packHash := strings.TrimSpace(string(run([]byte(strings.Join(hashes, "\n")+"\n"), "pack-objects", "--delta-base-offset", "objects/pack/pack")))
			run(nil, "prune-packed")
			packed := run(nil, "verify-pack", "-v", filepath.Join(path, "objects/pack/pack-"+packHash+".idx"))
			deltas := 0
			for line := range strings.SplitSeq(string(packed), "\n") {
				if len(strings.Fields(line)) == 7 {
					deltas++
				}
			}
			require.Positive(t, deltas)
			r, err := git.PlainOpen(path)
			require.NoError(t, err)
			defer r.Close()
			for _, test := range []struct {
				typ   plumbing.ObjectType
				count int
			}{
				{plumbing.BlobObject, 1}, {plumbing.TreeObject, 16}, {plumbing.CommitObject, 0}, {plumbing.TagObject, 0},
			} {
				iter, err := r.Storer.IterEncodedObjects(test.typ)
				require.NoError(t, err)
				count := 0
				require.NoError(t, iter.ForEach(func(obj plumbing.EncodedObject) error {
					count++
					require.Equal(t, test.typ, obj.Type())
					reader, err := obj.Reader()
					require.NoError(t, err)
					data, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.Equal(t, want[obj.Hash().String()], data)
					return nil
				}))
				iter.Close()
				require.Equal(t, test.count, count)
			}
		})
	}
}
