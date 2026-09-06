package packfile

import (
	"fmt"
	"io"
	"math"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	packutil "github.com/go-git/go-git/v6/plumbing/format/packfile/util"
)

// ObjectInfo describes an encoded object's identity, resolved type, and size.
type ObjectInfo struct {
	Hash   plumbing.Hash
	Type   plumbing.ObjectType
	Size   int64
	Offset int64

	pack   plumbing.Hash
	header *ObjectHeader
	object plumbing.EncodedObject
}

// EncodedObject returns the object when enumeration could create it without
// delta reconstruction.
func (i ObjectInfo) EncodedObject() (plumbing.EncodedObject, bool) {
	return i.object, i.object != nil
}

// ObjectInfoIter iterates encoded-object metadata.
type ObjectInfoIter interface {
	Next() (ObjectInfo, error)
	ForEach(func(ObjectInfo) error) error
	Close()
}

type objectInfoIter struct {
	p     *Packfile
	typ   plumbing.ObjectType
	iter  idxfile.EntryIter
	types *objectTypeCache
}

func (i *objectInfoIter) Next() (ObjectInfo, error) {
	if err := i.p.init(); err != nil {
		return ObjectInfo{}, err
	}

	i.p.m.Lock()
	defer i.p.m.Unlock()

	for {
		e, err := i.iter.Next()
		if err != nil {
			return ObjectInfo{}, err
		}

		if obj, ok := i.p.cache.Get(e.Hash); ok {
			typ := obj.Type()
			i.types.put(int64(e.Offset), typ)
			if i.typ == plumbing.AnyObject || typ == i.typ {
				return ObjectInfo{Hash: e.Hash, Type: typ, Size: obj.Size(), Offset: int64(e.Offset), pack: i.p.id, object: obj}, nil
			}
			continue
		}

		header, err := i.p.headerFromOffset(int64(e.Offset), e.Hash)
		if err != nil {
			return ObjectInfo{}, err
		}
		typ, err := i.objectType(header)
		if err != nil {
			return ObjectInfo{}, err
		}
		i.types.put(int64(e.Offset), typ)
		if i.typ != plumbing.AnyObject && typ != i.typ {
			continue
		}

		size := header.Size
		var object plumbing.EncodedObject
		if header.Type.IsDelta() {
			size, err = i.p.deltaTargetSize(header.ContentOffset)
			if err != nil {
				return ObjectInfo{}, err
			}
		} else {
			object, err = i.p.objectFromHeader(header)
			if err != nil {
				return ObjectInfo{}, err
			}
		}
		return ObjectInfo{
			Hash: e.Hash, Type: typ, Size: size, Offset: int64(e.Offset),
			pack: i.p.id, header: header, object: object,
		}, nil
	}
}

func (i *objectInfoIter) objectType(header *ObjectHeader) (plumbing.ObjectType, error) {
	objectIter := objectIter{p: i.p, types: i.types}
	return objectIter.objectType(header)
}

func (i *objectInfoIter) ForEach(f func(ObjectInfo) error) error {
	for {
		info, err := i.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := f(info); err != nil {
			return err
		}
	}
}

func (i *objectInfoIter) Close() {
	i.p.m.Lock()
	defer i.p.m.Unlock()

	_ = i.iter.Close()
	i.types = nil
}

type singleByteReader struct {
	r io.Reader
	b [1]byte
}

func (r *singleByteReader) ReadByte() (byte, error) {
	_, err := io.ReadFull(r.r, r.b[:])
	return r.b[0], err
}

func readDeltaTargetSize(r io.Reader) (int64, error) {
	br := singleByteReader{r: r}
	if _, err := packutil.DecodeLEB128FromReader(&br); err != nil {
		return 0, err
	}
	target, err := packutil.DecodeLEB128FromReader(&br)
	if err != nil {
		return 0, err
	}
	if uint64(target) > math.MaxInt64 {
		return 0, fmt.Errorf("%w: delta target size overflows int64", ErrMalformedPackfile)
	}
	return int64(target), nil
}
