package packfile

import (
	"fmt"
	"slices"
	"sort"

	"github.com/go-git/go-git/v6/plumbing"
)

type objectTypeCache struct {
	offsets []int64
	types   []plumbing.ObjectType
	pending map[int64]plumbing.ObjectType
}

func (c *objectTypeCache) get(offset int64) (plumbing.ObjectType, bool) {
	n := sort.Search(len(c.offsets), func(n int) bool {
		return c.offsets[n] >= offset
	})
	if n < len(c.offsets) && c.offsets[n] == offset {
		return c.types[n], true
	}
	typ, ok := c.pending[offset]
	return typ, ok
}

func (c *objectTypeCache) put(offset int64, typ plumbing.ObjectType) {
	if len(c.offsets) == 0 || offset > c.offsets[len(c.offsets)-1] {
		c.offsets = append(c.offsets, offset)
		c.types = append(c.types, typ)
		delete(c.pending, offset)
		return
	}
	if offset == c.offsets[len(c.offsets)-1] {
		c.types[len(c.types)-1] = typ
		return
	}
	c.putPending(offset, typ)
}

func (c *objectTypeCache) putPending(offset int64, typ plumbing.ObjectType) {
	if c.pending == nil {
		c.pending = make(map[int64]plumbing.ObjectType)
	}
	c.pending[offset] = typ
}

func (i *objectIter) objectType(header *ObjectHeader) (plumbing.ObjectType, error) {
	if !header.Type.IsDelta() {
		return header.Type, nil
	}
	var initial [8]int64
	chain := initial[:0]
	var typ plumbing.ObjectType
	for header.Type.IsDelta() {
		if cached, ok := i.types.get(header.Offset); ok {
			typ = cached
			break
		}
		if slices.Contains(chain, header.Offset) {
			return plumbing.InvalidObject, fmt.Errorf("%w: cyclic delta chain", ErrMalformedPackfile)
		}
		if len(chain) >= maxDeltaChainDepth {
			return plumbing.InvalidObject, fmt.Errorf(
				"%w: delta chain depth exceeds %d", ErrMalformedPackfile, maxDeltaChainDepth,
			)
		}
		chain = append(chain, header.Offset)

		var offset int64
		var hash plumbing.Hash
		var err error
		switch header.Type {
		case plumbing.REFDeltaObject:
			offset, err = i.p.FindOffset(header.Reference)
			if err != nil {
				return plumbing.InvalidObject, err
			}
			hash = header.Reference
		case plumbing.OFSDeltaObject:
			offset = header.OffsetReference
		}
		if cached, ok := i.types.get(offset); ok {
			typ = cached
			break
		}
		if header.Type == plumbing.OFSDeltaObject {
			hash, err = i.p.FindHash(offset)
			if err != nil {
				return plumbing.InvalidObject, err
			}
		}
		if base, ok := i.p.cache.Get(hash); ok {
			typ = base.Type()
			break
		}
		header, err = i.p.headerFromOffset(offset, hash)
		if err != nil {
			return plumbing.InvalidObject, err
		}
	}
	if typ == plumbing.InvalidObject {
		typ = header.Type
	}
	for n := 1; n < len(chain); n++ {
		i.types.putPending(chain[n], typ)
	}
	return typ, nil
}
