package filesystem

import (
	"bytes"
	"io"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
)

func TestPackedReaderSurvivesIteratorClose(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"sha1", "sha256"} {
		for _, mapped := range []bool{false, true} {
			name := format + "/fd"
			if mapped {
				name = format + "/mmap"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				run := func(input []byte, args ...string) []byte {
					cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
					cmd.Stdin = bytes.NewReader(input)
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					return out
				}
				run(nil, "init", "--bare", "--object-format="+format)
				content := make([]byte, 512<<10)
				rng := rand.New(rand.NewPCG(1, 2))
				for i := range content {
					content[i] = byte(rng.Uint32())
				}
				oid := run(content, "hash-object", "-w", "--stdin")
				run(oid, "pack-objects", "objects/pack/pack")
				run(nil, "prune-packed")
				require.Equal(t, content, run(nil, "cat-file", "blob", strings.TrimSpace(string(oid))))
				var opts []osfs.Option
				if mapped {
					opts = append(opts, osfs.WithMmap())
				}
				storage := NewStorage(osfs.New(dir, opts...), cache.NewObjectLRUDefault())
				t.Cleanup(func() { require.NoError(t, storage.Close()) })
				iter, err := storage.IterEncodedObjects(plumbing.BlobObject)
				require.NoError(t, err)
				t.Cleanup(iter.Close)
				obj, err := iter.Next()
				require.NoError(t, err)
				reader, err := obj.Reader()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, reader.Close()) })
				_, err = iter.Next()
				require.ErrorIs(t, err, io.EOF)
				iter.Close()
				got, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.Equal(t, content, got)
				reader2, err := obj.Reader()
				require.NoError(t, err)
				defer func() { require.NoError(t, reader2.Close()) }()
				got, err = io.ReadAll(reader2)
				require.NoError(t, err)
				require.Equal(t, content, got)
			})
		}
	}
}
