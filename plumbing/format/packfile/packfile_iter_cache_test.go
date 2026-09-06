package packfile_test

import (
	"io"
	"testing"

	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestObjectIteratorReusesCachedObjects(t *testing.T) {
	t.Parallel()
	p := newPackfile(t, fixtures.Basic().One())
	defer p.Close()
	first, err := p.GetAll()
	require.NoError(t, err)
	cached := make(map[plumbing.Hash]plumbing.EncodedObject)
	require.NoError(t, first.ForEach(func(o plumbing.EncodedObject) error {
		cached[o.Hash()] = o
		return nil
	}))
	first.Close()
	for _, typ := range []plumbing.ObjectType{plumbing.AnyObject, plumbing.BlobObject, plumbing.TreeObject, plumbing.CommitObject, plumbing.TagObject} {
		iter, err := p.GetByType(typ)
		require.NoError(t, err)
		count := 0
		for {
			o, err := iter.Next()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			require.Same(t, cached[o.Hash()], o)
			count++
		}
		iter.Close()
		want := 0
		for _, o := range cached {
			if typ == plumbing.AnyObject || o.Type() == typ {
				want++
			}
		}
		require.Equal(t, want, count)
	}
}
