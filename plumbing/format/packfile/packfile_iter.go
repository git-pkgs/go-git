package packfile

import (
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

type objectIter struct {
	p     *Packfile
	typ   plumbing.ObjectType
	iter  idxfile.EntryIter
	types *objectTypeCache
}

func (i *objectIter) Next() (plumbing.EncodedObject, error) {
	if err := i.p.init(); err != nil {
		return nil, err
	}

	i.p.m.Lock()
	defer i.p.m.Unlock()

	return i.next()
}

func (i *objectIter) next() (plumbing.EncodedObject, error) {
	for {
		e, err := i.iter.Next()
		if err != nil {
			return nil, err
		}

		if o, ok := i.p.cache.Get(e.Hash); ok {
			if i.types != nil {
				i.types.put(int64(e.Offset), o.Type())
			}
			if i.typ == plumbing.AnyObject || o.Type() == i.typ {
				return o, nil
			}
			continue
		}

		oh, err := i.p.headerFromOffset(int64(e.Offset), e.Hash)
		if err != nil {
			return nil, err
		}

		if i.typ == plumbing.AnyObject {
			return i.p.objectFromHeader(oh)
		}

		typ, err := i.objectType(oh)
		if err != nil {
			return nil, err
		}
		i.types.put(int64(e.Offset), typ)

		if typ == i.typ {
			return i.p.objectFromHeader(oh)
		}

		continue
	}
}

func (i *objectIter) ForEach(f func(plumbing.EncodedObject) error) error {
	if err := i.p.init(); err != nil {
		return err
	}

	i.p.m.Lock()
	defer i.p.m.Unlock()

	for {
		o, err := i.next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if err := f(o); err != nil {
			return err
		}
	}
}

func (i *objectIter) Close() {
	i.p.m.Lock()
	defer i.p.m.Unlock()

	_ = i.iter.Close()
	i.types = nil
}
