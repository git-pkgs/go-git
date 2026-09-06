package packfile

import (
	"bufio"
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"sync"
	"sync/atomic"

	billy "github.com/go-git/go-billy/v6"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	packutil "github.com/go-git/go-git/v6/plumbing/format/packfile/util"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/utils/binary"
	"github.com/go-git/go-git/v6/utils/ioutil"
	gogitsync "github.com/go-git/go-git/v6/utils/sync"
)

var (
	// ErrInvalidObject is returned by Decode when an invalid object is
	// found in the packfile.
	ErrInvalidObject = NewError("invalid git object")
	// ErrZLib is returned by Decode when there was an error unzipping
	// the packfile contents.
	ErrZLib = NewError("zlib reading error")
)

const objectHeaderViewSize = 64

type viewReaderAt interface {
	ViewAt(int64, int, func([]byte) error) (int, error)
}

type viewReaderFrom interface {
	ViewFrom(int64, func([]byte) error) error
}

type knownHashObjectCache interface {
	PutWithHash(plumbing.Hash, plumbing.EncodedObject)
}

// Packfile allows retrieving information from inside a packfile.
type Packfile struct {
	idxfile.Index
	fs   billy.Filesystem
	file billy.File

	// handle is the resolved PackHandle once init has run; nil
	// in legacy mode. See NewPackfile for the modes.
	handle        PackHandle
	resolveHandle PackHandleResolver

	scanReader io.ReadSeekCloser
	scanner    *Scanner

	cache cache.Object
	rbuf  *bufio.Reader
	view  bytes.Reader

	id           plumbing.Hash
	m            sync.Mutex
	objectIDSize int

	once    sync.Once
	onceErr error

	closed atomic.Bool
}

// NewPackfile returns a packfile representation for the given .pack
// file and idx. If [WithFs] is set the packfile returns [FSObject]s;
// otherwise it returns [plumbing.MemoryObject]s.
// Non-delta FSObject payloads are decoded and validated by Reader.
//
// When [WithPackHandle] is supplied, the resolver owns the pack
// file descriptor and the file argument is redundant; the
// constructor closes it and [Packfile.Close] does not close the
// resolver-owned handle. Otherwise the file argument is used as-is
// and is closed by [Packfile.Close].
func NewPackfile(
	file billy.File,
	opts ...PackfileOption,
) *Packfile {
	p := &Packfile{
		file:         file,
		objectIDSize: crypto.SHA1.Size(),
	}
	for _, opt := range opts {
		opt(p)
	}

	if p.resolveHandle != nil && file != nil {
		_ = file.Close()
		p.file = nil
	}

	return p
}

// Get retrieves the encoded object in the packfile with the given hash.
func (p *Packfile) Get(h plumbing.Hash) (plumbing.EncodedObject, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}
	p.m.Lock()
	defer p.m.Unlock()
	// Re-check after Lock: Close may have flipped closed and torn
	// down the scanner between the early Load and the Lock.
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}

	return p.get(h)
}

// GetByOffset retrieves the encoded object from the packfile at the given
// offset.
func (p *Packfile) GetByOffset(offset int64) (plumbing.EncodedObject, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}
	p.m.Lock()
	defer p.m.Unlock()
	// Re-check after Lock: Close may have flipped closed and torn
	// down the scanner between the early Load and the Lock.
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}

	return p.getByOffset(offset)
}

// GetByInfo retrieves an object described by GetObjectInfosByType.
func (p *Packfile) GetByInfo(info ObjectInfo) (plumbing.EncodedObject, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}
	p.m.Lock()
	defer p.m.Unlock()
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if !p.id.Equal(info.pack) {
		return nil, plumbing.ErrObjectNotFound
	}
	if info.object != nil {
		return info.object, nil
	}
	if info.header == nil {
		return nil, plumbing.ErrObjectNotFound
	}
	if obj, ok := p.cache.Get(info.Hash); ok {
		return obj, nil
	}
	obj, err := p.objectFromHeader(info.header)
	if err != nil {
		return nil, err
	}
	if obj.Type() != info.Type || obj.Size() != info.Size {
		return nil, plumbing.ErrObjectNotFound
	}
	return obj, nil
}

// GetSizeByOffset retrieves the size of the encoded object from the
// packfile with the given offset.
func (p *Packfile) GetSizeByOffset(offset int64) (size int64, err error) {
	if p.closed.Load() {
		return 0, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return 0, err
	}

	d, err := p.GetByOffset(offset)
	if err != nil {
		return 0, err
	}

	return d.Size(), nil
}

// GetAll returns an iterator with all encoded objects in the packfile.
// The iterator returned is not thread-safe, it should be used in the same
// thread as the Packfile instance.
func (p *Packfile) GetAll() (storer.EncodedObjectIter, error) {
	return p.GetByType(plumbing.AnyObject)
}

// GetByType returns all the objects of the given type.
func (p *Packfile) GetByType(typ plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}

	switch typ {
	case plumbing.AnyObject,
		plumbing.BlobObject,
		plumbing.TreeObject,
		plumbing.CommitObject,
		plumbing.TagObject:
		entries, err := p.EntriesByOffset()
		if err != nil {
			return nil, err
		}

		iter := &objectIter{
			p:    p,
			iter: entries,
			typ:  typ,
		}
		if typ != plumbing.AnyObject {
			iter.types = &objectTypeCache{}
		}
		return iter, nil
	default:
		return nil, plumbing.ErrInvalidType
	}
}

// GetObjectInfosByType returns metadata for all objects of the given type.
// Delta objects are classified and sized without reconstructing their bases.
func (p *Packfile) GetObjectInfosByType(typ plumbing.ObjectType) (ObjectInfoIter, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}

	switch typ {
	case plumbing.AnyObject,
		plumbing.BlobObject,
		plumbing.TreeObject,
		plumbing.CommitObject,
		plumbing.TagObject:
		entries, err := p.EntriesByOffset()
		if err != nil {
			return nil, err
		}
		return &objectInfoIter{
			p:     p,
			iter:  entries,
			typ:   typ,
			types: &objectTypeCache{},
		}, nil
	default:
		return nil, plumbing.ErrInvalidType
	}
}

// Scanner returns the Packfile's inner scanner.
//
// Deprecated: this will be removed in future versions of the packfile package
// to avoid exposing the package internals and to improve its thread-safety.
// TODO: Remove Scanner method
func (p *Packfile) Scanner() (*Scanner, error) {
	if p.closed.Load() {
		return nil, fs.ErrClosed
	}
	if err := p.init(); err != nil {
		return nil, err
	}

	return p.scanner, nil
}

// ID returns the ID of the packfile, which is the checksum at the end of it.
func (p *Packfile) ID() (plumbing.Hash, error) {
	if err := p.init(); err != nil {
		return plumbing.ZeroHash, err
	}

	return p.id, nil
}

// get is not threat-safe, and should only be called within packfile.go.
func (p *Packfile) get(h plumbing.Hash) (plumbing.EncodedObject, error) {
	if obj, ok := p.cache.Get(h); ok {
		return obj, nil
	}

	offset, err := p.FindOffset(h)
	if err != nil {
		return nil, err
	}

	oh, err := p.headerFromOffset(offset, h)
	if err != nil {
		return nil, err
	}

	return p.objectFromHeader(oh)
}

// getByOffset is not threat-safe, and should only be called within packfile.go.
func (p *Packfile) getByOffset(offset int64) (plumbing.EncodedObject, error) {
	h, err := p.FindHash(offset)
	if err != nil {
		return nil, err
	}

	if obj, ok := p.cache.Get(h); ok {
		return obj, nil
	}

	oh, err := p.headerFromOffset(offset, h)
	if err != nil {
		return nil, err
	}

	return p.objectFromHeader(oh)
}

func (p *Packfile) init() error {
	p.once.Do(func() {
		if p.handle == nil && p.resolveHandle != nil {
			h, err := p.resolveHandle()
			if err != nil {
				p.onceErr = fmt.Errorf("packfile: resolve pack handle: %w", err)
				return
			}
			p.handle = h
		}

		if p.handle == nil && p.file == nil {
			p.onceErr = fmt.Errorf("file is not set")
			return
		}

		if p.Index == nil {
			p.onceErr = fmt.Errorf("index is not set")
			return
		}

		p.rbuf = gogitsync.GetBufioReader(nil)

		opts := []ScannerOption{WithBufioReader(p.rbuf)}

		if p.objectIDSize == format.SHA256Size {
			opts = append(opts, WithSHA256())
		}

		var scanSrc io.Reader
		if p.handle != nil {
			r, err := p.handle.OpenPackReader()
			if err != nil {
				p.onceErr = fmt.Errorf("packfile: open pack reader: %w", err)
				return
			}
			p.scanReader = r
			scanSrc = r
		} else {
			scanSrc = p.file
		}

		p.scanner = NewScanner(scanSrc, opts...)
		// Validate packfile signature.
		if !p.scanner.Scan() {
			p.onceErr = p.scanner.Error()
			return
		}

		if p.handle != nil {
			id, err := p.handle.PackHash()
			if err != nil {
				p.onceErr = fmt.Errorf("packfile: read pack hash: %w", err)
				return
			}
			p.id = id
		} else {
			_, err := p.scanner.Seek(-int64(p.objectIDSize), io.SeekEnd)
			if err != nil {
				p.onceErr = err
				return
			}
			p.id.ResetBySize(p.objectIDSize)
			_, err = p.id.ReadFrom(p.scanner)
			if err != nil {
				p.onceErr = err
			}
		}

		if p.cache == nil {
			p.cache = cache.NewObjectLRUDefault()
		}
	})

	return p.onceErr
}

func (p *Packfile) headerFromOffset(offset int64, h plumbing.Hash) (*ObjectHeader, error) {
	if viewer, ok := p.scanReader.(viewReaderAt); ok {
		var oh *ObjectHeader
		_, err := viewer.ViewAt(offset, objectHeaderViewSize, func(data []byte) error {
			var err error
			oh, err = readObjectHeaderBytes(data, offset, p.objectIDSize)
			return err
		})
		if !errors.Is(err, errors.ErrUnsupported) {
			if err != nil && (err != io.EOF || oh == nil) {
				return nil, err
			}
			oh.Hash = h
			return oh, nil
		}
	}

	err := p.scanner.SeekFromStart(offset)
	if err != nil {
		return nil, err
	}

	oh, err := p.scanner.readObjectHeader()
	if err != nil {
		return nil, err
	}
	oh.Hash = h
	return oh, nil
}

func readObjectHeaderBytes(data []byte, offset int64, objectIDSize int) (*ObjectHeader, error) {
	pos := 0
	readByte := func() (byte, error) {
		if pos >= len(data) {
			return 0, io.EOF
		}
		b := data[pos]
		pos++
		return b, nil
	}

	first, err := readByte()
	if err != nil {
		return nil, err
	}
	typ := packutil.ObjectType(first)
	if !typ.Valid() {
		return nil, fmt.Errorf("%w: invalid object type: %v", ErrMalformedPackfile, first)
	}

	size := uint64(first & 0x0f)
	for shift := uint(4); first&0x80 != 0; shift += 7 {
		if shift > 64-7 {
			return nil, fmt.Errorf("%w: %w", ErrMalformedPackfile, packutil.ErrLengthOverflow)
		}
		first, err = readByte()
		if err != nil {
			return nil, err
		}
		size |= uint64(first&0x7f) << shift
	}

	oh := &ObjectHeader{
		Offset:   offset,
		Type:     typ,
		diskType: typ,
		Size:     int64(size),
	}
	if typ.IsDelta() {
		oh.Hash.ResetBySize(objectIDSize)
	}
	switch typ {
	case plumbing.OFSDeltaObject:
		c, err := readByte()
		if err != nil {
			return nil, err
		}
		base := int64(c & 0x7f)
		for c&0x80 != 0 {
			if base >= (math.MaxInt64-0x7f)>>7 {
				return nil, binary.ErrIntegerOverflow
			}
			base++
			c, err = readByte()
			if err != nil {
				return nil, err
			}
			base = (base << 7) + int64(c&0x7f)
		}
		if err := ValidateOFSDeltaBase(offset, base); err != nil {
			return nil, err
		}
		oh.OffsetReference = offset - base
	case plumbing.REFDeltaObject:
		oh.Reference.ResetBySize(objectIDSize)
		if len(data)-pos < objectIDSize {
			if len(data) == pos {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
		_, _ = oh.Reference.Write(data[pos : pos+objectIDSize])
		pos += objectIDSize
	}
	oh.ContentOffset = offset + int64(pos)
	return oh, nil
}

func (p *Packfile) inflateContent(contentOffset int64, writer io.Writer, declaredSize int64) error {
	viewer, ok := p.scanReader.(viewReaderFrom)
	if !ok {
		return p.scanner.inflateContent(contentOffset, writer, declaredSize)
	}
	err := viewer.ViewFrom(contentOffset, func(data []byte) error {
		p.view.Reset(data)
		defer p.view.Reset(nil)
		zr, err := gogitsync.GetZlibReader(&p.view)
		if err != nil {
			return fmt.Errorf("zlib reset error: %w", err)
		}
		defer gogitsync.PutZlibReader(zr)
		_, err = ioutil.CopyBufferPool(&boundedWriter{w: writer, limit: declaredSize}, zr)
		return err
	})
	if errors.Is(err, errors.ErrUnsupported) {
		return p.scanner.inflateContent(contentOffset, writer, declaredSize)
	}
	return err
}

func (p *Packfile) deltaTargetSize(contentOffset int64) (int64, error) {
	viewer, ok := p.scanReader.(viewReaderFrom)
	if ok {
		var size int64
		err := viewer.ViewFrom(contentOffset, func(data []byte) error {
			p.view.Reset(data)
			defer p.view.Reset(nil)
			zr, err := gogitsync.GetZlibReader(&p.view)
			if err != nil {
				return fmt.Errorf("zlib reset error: %w", err)
			}
			defer gogitsync.PutZlibReader(zr)
			size, err = readDeltaTargetSize(zr)
			return err
		})
		if !errors.Is(err, errors.ErrUnsupported) {
			return size, err
		}
	}

	return p.scanner.deltaTargetSize(contentOffset)
}

// Close the packfile and its resources. Subsequent calls to [Packfile.Get],
// [Packfile.GetByOffset], and the other entry points return [fs.ErrClosed].
// Close is idempotent.
func (p *Packfile) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	p.m.Lock()
	defer p.m.Unlock()

	gogitsync.PutBufioReader(p.rbuf)

	if p.handle != nil {
		// The resolver owns the handle; close only the scanner cursor.
		if p.scanReader != nil {
			err := p.scanReader.Close()
			p.scanReader = nil
			return err
		}
		return nil
	}

	closer, ok := p.file.(io.Closer)
	if !ok {
		return nil
	}

	return closer.Close()
}

func (p *Packfile) objectFromHeader(oh *ObjectHeader) (plumbing.EncodedObject, error) {
	if oh == nil {
		return nil, plumbing.ErrObjectNotFound
	}

	// If we have filesystem, and the object is not a delta type, return a FSObject.
	// This avoids having to inflate the object more than once.
	if !oh.Type.IsDelta() && p.fs != nil {
		var fsObj *FSObject
		if p.handle != nil {
			fsObj = &FSObject{
				hash:          oh.ID(),
				offset:        oh.ContentOffset,
				size:          oh.Size,
				typ:           oh.Type,
				index:         p.Index,
				fs:            p.fs,
				cache:         p.cache,
				acquireRandom: p.openRandomReader,
			}
		} else {
			fsObj = NewFSObject(
				oh.ID(),
				oh.Type,
				oh.ContentOffset,
				oh.Size,
				p.Index,
				p.fs,
				p.file,
				p.file.Name(),
				p.cache,
			)
		}

		p.cache.Put(fsObj)
		return fsObj, nil
	}

	return p.getMemoryObject(oh)
}

func (p *Packfile) putObject(hash plumbing.Hash, object plumbing.EncodedObject) {
	if cache, ok := p.cache.(knownHashObjectCache); ok {
		cache.PutWithHash(hash, object)
		return
	}
	p.cache.Put(object)
}

func (p *Packfile) getMemoryObject(oh *ObjectHeader) (plumbing.EncodedObject, error) {
	of := format.SHA1
	if p.objectIDSize == format.SHA256.Size() {
		of = format.SHA256
	}
	h := plumbing.FromObjectFormat(of)
	obj := plumbing.NewMemoryObject(h)

	obj.SetSize(oh.Size)
	obj.SetType(oh.Type)

	w, err := obj.Writer()
	if err != nil {
		return nil, err
	}
	defer ioutil.CheckClose(w, &err)

	switch oh.Type {
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
		err = p.inflateContent(oh.ContentOffset, w, oh.Size)

	case plumbing.REFDeltaObject, plumbing.OFSDeltaObject:
		var parent plumbing.EncodedObject

		switch oh.Type {
		case plumbing.REFDeltaObject:
			var ok bool
			parent, ok = p.cache.Get(oh.Reference)
			if !ok {
				parent, err = p.get(oh.Reference)
			}
		case plumbing.OFSDeltaObject:
			parent, err = p.getByOffset(oh.OffsetReference)
		}

		if err != nil {
			return nil, fmt.Errorf("cannot find base object: %w", err)
		}

		delta := gogitsync.GetBytesBuffer()
		defer gogitsync.PutBytesBuffer(delta)
		err = p.inflateContent(oh.ContentOffset, delta, oh.Size)
		if err != nil {
			return nil, fmt.Errorf("cannot inflate content: %w", err)
		}

		obj.SetType(parent.Type())
		err = ApplyDelta(obj, parent, delta)

	default:
		err = ErrInvalidObject.AddDetails("type %q", oh.Type)
	}

	if err != nil {
		return nil, err
	}

	p.putObject(oh.ID(), obj)

	return obj, nil
}
