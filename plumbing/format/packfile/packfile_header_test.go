package packfile

import (
	"encoding/binary"
	"io"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

type countedPackFile struct {
	*os.File
	bytesRead int
}

func (f *countedPackFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.bytesRead += n
	return n, err
}

func TestPackedBlobEnumerationDoesNotInflatePayload(t *testing.T) {
	t.Parallel()
	content := make([]byte, 1<<20)
	random := rand.NewPCG(1, 2)
	for i := 0; i < len(content); i += 8 {
		binary.LittleEndian.PutUint64(content[i:], random.Uint64())
	}
	data, offsets := buildTestPack(t, testPackObject{typ: plumbing.BlobObject, content: content})
	file := &countedPackFile{File: writeTestPackFile(t, data)}
	h := testObjectHash(plumbing.BlobObject, content)
	p := NewPackfile(file, WithFs(osfs.New(t.TempDir())), WithIdx(&singleEntryIndex{
		entry: &idxfile.Entry{Hash: h, Offset: uint64(offsets[0])},
	}))
	defer p.Close()
	iter, err := p.GetByType(plumbing.BlobObject)
	require.NoError(t, err)
	defer iter.Close()
	o, err := iter.Next()
	require.NoError(t, err)
	require.Equal(t, h, o.Hash())
	require.Equal(t, int64(len(content)), o.Size())
	require.Less(t, file.bytesRead, len(data)/2)
	r, err := o.Reader()
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.Equal(t, content, got)
}

func TestPackedBlobReaderRejectsPayloadOverrun(t *testing.T) {
	t.Parallel()
	content := []byte("longer than declared")
	data, offsets := buildTestPack(t, testPackObject{typ: plumbing.BlobObject, declaredSize: 1, content: content})
	h := testObjectHash(plumbing.BlobObject, content)
	p := NewPackfile(writeTestPackFile(t, data), WithFs(osfs.New(t.TempDir())), WithIdx(&singleEntryIndex{
		entry: &idxfile.Entry{Hash: h, Offset: uint64(offsets[0])},
	}))
	defer p.Close()
	o, err := p.Get(h)
	require.NoError(t, err)
	r, err := o.Reader()
	require.NoError(t, err)
	defer r.Close()
	_, err = io.ReadAll(r)
	require.Error(t, err)
}
