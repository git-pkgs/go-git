package packfile

import (
	"encoding/hex"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

func TestBlobIteratorSkipsDeltaTreePayload(t *testing.T) {
	t.Parallel()
	content := make([]byte, 512<<10)
	random := rand.New(rand.NewPCG(1, 2))
	for i := range content {
		content[i] = byte(random.Uint32())
	}
	baseHash := testObjectHash(plumbing.TreeObject, content)
	result := append([]byte(nil), content...)
	result[0] ^= 1
	var ops [][]byte
	for rest := result; len(rest) != 0; {
		n := min(127, len(rest))
		ops = append(ops, insertOp(rest[:n]))
		rest = rest[n:]
	}
	delta := buildDelta(len(content), len(result), ops...)
	for _, forward := range []bool{false, true} {
		name := "backward"
		if forward {
			name = "forward"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			objects := []testPackObject{
				{typ: plumbing.TreeObject, content: content},
				{typ: plumbing.REFDeltaObject, content: delta, reference: baseHash},
				{typ: plumbing.BlobObject, content: []byte("blob")},
			}
			hashes := []plumbing.Hash{baseHash, testObjectHash(plumbing.TreeObject, result), testObjectHash(plumbing.BlobObject, []byte("blob"))}
			if forward {
				objects[0], objects[1] = objects[1], objects[0]
				hashes[0], hashes[1] = hashes[1], hashes[0]
			}
			data, offsets := buildTestPack(t, objects...)
			var writer idxfile.Writer
			require.NoError(t, writer.OnHeader(uint32(len(hashes))))
			for n, h := range hashes {
				writer.Add(h, uint64(offsets[n]), 0)
			}
			require.NoError(t, writer.OnFooter(plumbing.NewHash(hex.EncodeToString(data[len(data)-baseHash.Size():]))))
			index, err := writer.Index()
			require.NoError(t, err)
			file := &countedPackFile{File: writeTestPackFile(t, data)}
			p := NewPackfile(file, WithIdx(index))
			defer p.Close()
			iter, err := p.GetByType(plumbing.BlobObject)
			require.NoError(t, err)
			defer iter.Close()
			obj, err := iter.Next()
			require.NoError(t, err)
			require.Equal(t, hashes[2], obj.Hash())
			_, err = iter.Next()
			require.ErrorIs(t, err, io.EOF)
			require.Less(t, file.bytesRead, len(content)/2)
			obj, err = p.Get(testObjectHash(plumbing.TreeObject, result))
			require.NoError(t, err)
			reader, err := obj.Reader()
			require.NoError(t, err)
			got, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			require.Equal(t, result, got)
		})
	}
}

func TestFilteredIteratorRejectsDeltaCycle(t *testing.T) {
	t.Parallel()
	h := plumbing.NewHash("1111111111111111111111111111111111111111")
	data, offsets := buildTestPack(t, testPackObject{typ: plumbing.REFDeltaObject, reference: h, content: buildDelta(0, 0)})
	p := NewPackfile(writeTestPackFile(t, data), WithIdx(&singleEntryIndex{entry: &idxfile.Entry{Hash: h, Offset: uint64(offsets[0])}}))
	defer p.Close()
	iter, err := p.GetByType(plumbing.BlobObject)
	require.NoError(t, err)
	defer iter.Close()
	_, err = iter.Next()
	require.ErrorIs(t, err, ErrMalformedPackfile)
	require.ErrorContains(t, err, "cyclic delta chain")
}

func TestObjectTypeCacheKeepsVisitedOffsetsOrdered(t *testing.T) {
	t.Parallel()

	var cache objectTypeCache
	cache.put(10, plumbing.BlobObject)
	cache.putPending(30, plumbing.TreeObject)
	cache.put(20, plumbing.CommitObject)

	typ, ok := cache.get(30)
	require.True(t, ok)
	require.Equal(t, plumbing.TreeObject, typ)

	cache.put(30, typ)
	require.Equal(t, []int64{10, 20, 30}, cache.offsets)
	require.Empty(t, cache.pending)
}
