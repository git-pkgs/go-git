package filesystem

import (
	"errors"
	"io"
	"io/fs"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/objfile"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
)

// ObjectInfo describes an encoded object's identity, resolved type, and size.
// Location fields are retained privately for deferred object readers.
type ObjectInfo struct {
	Hash plumbing.Hash
	Type plumbing.ObjectType
	Size int64

	packHash plumbing.Hash
	packInfo *packfile.ObjectInfo
	offset   int64
	packed   bool
	object   plumbing.EncodedObject
}

// ObjectInfoIter iterates encoded-object metadata.
type ObjectInfoIter interface {
	Next() (ObjectInfo, error)
	ForEach(func(ObjectInfo) error) error
	Close()
}

// ObjectInfoReader loads objects returned by an [ObjectInfoIter] while reusing
// pack readers. It is not safe for concurrent use.
type ObjectInfoReader struct {
	storage *ObjectStorage
	packs   map[plumbing.Hash]*packfile.Packfile
	closed  bool
}

// NewObjectInfoReader returns a reader for deferred object metadata.
func (s *ObjectStorage) NewObjectInfoReader() *ObjectInfoReader {
	return &ObjectInfoReader{storage: s, packs: make(map[plumbing.Hash]*packfile.Packfile)}
}

// EncodedObject loads an object previously returned by IterObjectInfos.
func (r *ObjectInfoReader) EncodedObject(info ObjectInfo) (plumbing.EncodedObject, error) {
	if r.closed {
		return nil, fs.ErrClosed
	}
	if info.object != nil {
		return info.object, nil
	}
	if !info.packed {
		obj, err := r.storage.getFromUnpacked(info.Hash)
		if err != nil {
			return nil, err
		}
		if obj.Type() != info.Type {
			return nil, plumbing.ErrObjectNotFound
		}
		return obj, nil
	}
	if cached, ok := r.storage.objectCache.Get(info.Hash); ok {
		if cached.Type() != info.Type {
			return nil, plumbing.ErrObjectNotFound
		}
		return cached, nil
	}
	pack := r.packs[info.packHash]
	if pack == nil {
		if err := r.storage.requireIndex(); err != nil {
			return nil, err
		}
		r.storage.muI.RLock()
		idx := r.storage.index[info.packHash]
		r.storage.muI.RUnlock()
		if idx == nil {
			return nil, plumbing.ErrObjectNotFound
		}
		var err error
		pack, err = r.storage.packfile(idx, info.packHash)
		if err != nil {
			return nil, err
		}
		r.packs[info.packHash] = pack
	}
	if info.packInfo == nil {
		return nil, plumbing.ErrObjectNotFound
	}
	obj, err := pack.GetByInfo(*info.packInfo)
	if err != nil {
		return nil, err
	}
	if obj.Type() != info.Type {
		return nil, plumbing.ErrObjectNotFound
	}
	return obj, nil
}

// Close releases the reader's pack resources.
func (r *ObjectInfoReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	for hash, pack := range r.packs {
		errs = append(errs, pack.Close())
		delete(r.packs, hash)
	}
	return errors.Join(errs...)
}

type looseObjectInfoIter struct {
	s      *ObjectStorage
	typ    plumbing.ObjectType
	hashes []plumbing.Hash
}

func (i *looseObjectInfoIter) Next() (info ObjectInfo, err error) {
	for len(i.hashes) != 0 {
		hash := i.hashes[0]
		i.hashes = i.hashes[1:]
		file, err := i.s.dir.Object(hash)
		if err != nil {
			return ObjectInfo{}, err
		}
		reader, err := objfile.NewReader(file, i.s.options.ObjectFormat)
		if err != nil {
			_ = file.Close()
			return ObjectInfo{}, err
		}
		typ, size, err := reader.Header()
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return ObjectInfo{}, err
		}
		if i.typ == plumbing.AnyObject || typ == i.typ {
			return ObjectInfo{Hash: hash, Type: typ, Size: size}, nil
		}
	}
	return ObjectInfo{}, io.EOF
}

func (i *looseObjectInfoIter) ForEach(f func(ObjectInfo) error) error {
	return forEachObjectInfo(i, f)
}

func (i *looseObjectInfoIter) Close() {
	i.hashes = nil
}

type lazyPackfileInfoIter struct {
	hashes []plumbing.Hash
	open   func(plumbing.Hash) (ObjectInfoIter, error)
	cur    ObjectInfoIter
}

func (i *lazyPackfileInfoIter) Next() (ObjectInfo, error) {
	for {
		if i.cur == nil {
			if len(i.hashes) == 0 {
				return ObjectInfo{}, io.EOF
			}
			hash := i.hashes[0]
			i.hashes = i.hashes[1:]
			iter, err := i.open(hash)
			if err == io.EOF {
				continue
			}
			if err != nil {
				return ObjectInfo{}, err
			}
			i.cur = iter
		}
		info, err := i.cur.Next()
		if err == io.EOF {
			i.cur.Close()
			i.cur = nil
			continue
		}
		if err != nil {
			i.cur.Close()
			i.cur = nil
			return ObjectInfo{}, err
		}
		return info, nil
	}
}

func (i *lazyPackfileInfoIter) ForEach(f func(ObjectInfo) error) error {
	return forEachObjectInfo(i, f)
}

func (i *lazyPackfileInfoIter) Close() {
	if i.cur != nil {
		i.cur.Close()
		i.cur = nil
	}
	i.hashes = nil
}

type packfileObjectInfoIter struct {
	pack     io.Closer
	packHash plumbing.Hash
	iter     packfile.ObjectInfoIter
	seen     map[plumbing.Hash]struct{}
}

func (i *packfileObjectInfoIter) Next() (ObjectInfo, error) {
	for {
		info, err := i.iter.Next()
		if err != nil {
			return ObjectInfo{}, err
		}
		if _, ok := i.seen[info.Hash]; ok {
			continue
		}
		i.seen[info.Hash] = struct{}{}
		object := objectFromPackInfo(info)
		var packInfo *packfile.ObjectInfo
		if object == nil {
			packInfo = retainPackInfo(info)
		}
		return ObjectInfo{
			Hash: info.Hash, Type: info.Type, Size: info.Size,
			packHash: i.packHash, offset: info.Offset, packed: true,
			packInfo: packInfo, object: object,
		}, nil
	}
}

func retainPackInfo(info packfile.ObjectInfo) *packfile.ObjectInfo {
	return &info
}

func objectFromPackInfo(info packfile.ObjectInfo) plumbing.EncodedObject {
	object, _ := info.EncodedObject()
	return object
}

func (i *packfileObjectInfoIter) ForEach(f func(ObjectInfo) error) error {
	return forEachObjectInfo(i, f)
}

func (i *packfileObjectInfoIter) Close() {
	i.iter.Close()
	_ = i.pack.Close()
}

type multiObjectInfoIter struct {
	iters []ObjectInfoIter
}

func (i *multiObjectInfoIter) Next() (ObjectInfo, error) {
	for len(i.iters) != 0 {
		info, err := i.iters[0].Next()
		if err == io.EOF {
			i.iters[0].Close()
			i.iters = i.iters[1:]
			continue
		}
		return info, err
	}
	return ObjectInfo{}, io.EOF
}

func (i *multiObjectInfoIter) ForEach(f func(ObjectInfo) error) error {
	return forEachObjectInfo(i, f)
}

func (i *multiObjectInfoIter) Close() {
	for _, iter := range i.iters {
		iter.Close()
	}
	i.iters = nil
}

func forEachObjectInfo(iter ObjectInfoIter, f func(ObjectInfo) error) error {
	defer iter.Close()
	for {
		info, err := iter.Next()
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
