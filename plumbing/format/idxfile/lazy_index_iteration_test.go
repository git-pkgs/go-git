package idxfile

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type countingIndexReader struct {
	*bytes.Reader
	reads int
}

func TestLazyIndexReverseIterationErrors(t *testing.T) {
	t.Parallel()
	const count = 1025
	data := buildMinimalIdx(count, 20)
	for _, test := range []struct {
		name string
		rev  []byte
		want error
	}{
		{"truncated", buildMinimalRev(count, 20)[:revHeaderSize+count*4-1], io.ErrUnexpectedEOF},
		{"out of range", func() []byte {
			rev := buildMinimalRev(count, 20)
			binary.BigEndian.PutUint32(rev[revHeaderSize+(count-1)*4:], count)
			return rev
		}(), ErrMalformedIdxFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			idx, err := NewLazyIndex(readerAtOpener(data), readerAtOpener(test.rev), extractPackHash(data, 20))
			require.NoError(t, err)
			defer idx.Close()
			iter, err := idx.EntriesByOffset()
			require.NoError(t, err)
			defer iter.Close()
			for range count - 1 {
				_, err = iter.Next()
				require.NoError(t, err)
			}
			_, err = iter.Next()
			require.ErrorIs(t, err, test.want)
		})
	}
}

func (r *countingIndexReader) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	return r.Reader.ReadAt(p, off)
}

func (*countingIndexReader) Close() error { return nil }

func TestLazyIndexBatchesReverseIteration(t *testing.T) {
	t.Parallel()
	const count = 4097
	for _, size := range []int{20, 32} {
		data := buildMinimalIdx(count, size)
		reverseData := buildMinimalRev(count, size)
		for pos := range count {
			idxPos := (pos * 13) % count
			binary.BigEndian.PutUint32(reverseData[revHeaderSize+pos*4:], uint32(idxPos))
			binary.BigEndian.PutUint32(data[idxHeaderSize+idxFanoutSize+count*(size+4)+idxPos*4:], uint32(pos*100))
			binary.BigEndian.PutUint32(data[idxHeaderSize+idxFanoutSize+count*size+idxPos*4:], uint32(pos))
		}
		indexReader := &countingIndexReader{Reader: bytes.NewReader(data)}
		rev := &countingIndexReader{Reader: bytes.NewReader(reverseData)}
		idx, err := NewLazyIndex(func() (ReadAtCloser, error) { return indexReader, nil }, func() (ReadAtCloser, error) {
			return rev, nil
		}, extractPackHash(data, size))
		require.NoError(t, err)
		defer idx.Close()
		iter, err := idx.EntriesByOffset()
		require.NoError(t, err)
		rev.reads = 0
		indexReader.reads = 0
		retained := make([]*Entry, 0, count)
		for pos := range count {
			entry, err := iter.Next()
			require.NoError(t, err)
			require.Equal(t, uint64(pos*100), entry.Offset)
			require.Equal(t, uint32(pos), entry.CRC32)
			want := make([]byte, size)
			idxPos := (pos * 13) % count
			want[1], want[2] = byte(idxPos>>8), byte(idxPos)
			require.Equal(t, want, entry.Hash.Bytes())
			retained = append(retained, entry)
		}
		_, err = iter.Next()
		require.ErrorIs(t, err, io.EOF)
		require.NoError(t, iter.Close())
		require.NoError(t, iter.Close())
		require.Less(t, rev.reads, 10)
		require.Less(t, indexReader.reads, 100)
		for pos, entry := range retained {
			require.Equal(t, uint64(pos*100), entry.Offset)
		}
	}
}
