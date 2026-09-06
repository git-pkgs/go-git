package cache

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/go-git/go-git/v6/plumbing"
)

type ObjectSuite struct {
	suite.Suite
	c       map[string]Object
	aObject plumbing.EncodedObject
	bObject plumbing.EncodedObject
	cObject plumbing.EncodedObject
	dObject plumbing.EncodedObject
	eObject plumbing.EncodedObject
}

func TestObjectSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ObjectSuite))
}

func (s *ObjectSuite) SetupTest() {
	s.aObject = newObject("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1*Byte)
	s.bObject = newObject("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 3*Byte)
	s.cObject = newObject("cccccccccccccccccccccccccccccccccccccccc", 1*Byte)
	s.dObject = newObject("dddddddddddddddddddddddddddddddddddddddd", 1*Byte)
	s.eObject = newObject("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", 2*Byte)

	s.c = make(map[string]Object)
	s.c["two_bytes"] = NewObjectLRU(2 * Byte)
	s.c["default_lru"] = NewObjectLRUDefault()
	s.c["sharded_two_bytes"] = NewShardedObjectLRU(2*Byte, 2)
}

func (s *ObjectSuite) TestPutSameObject() {
	for _, o := range s.c {
		o.Put(s.aObject)
		o.Put(s.aObject)
		_, ok := o.Get(s.aObject.Hash())
		s.True(ok)
	}
}

func (s *ObjectSuite) TestPutWithHashDoesNotComputeHash() {
	cache := NewObjectLRU(2 * Byte)
	want := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	object := &countingObject{size: 1}
	cache.PutWithHash(want, object)
	got, ok := cache.Get(want)
	s.True(ok)
	s.Same(object, got)
	cache.PutWithHash(plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), &countingObject{size: 2})
	s.Zero(object.hashCalls)
}

func (s *ObjectSuite) TestPutWithHashSupportsObjectFormats() {
	cache := NewObjectLRU(2 * Byte)
	sha1 := plumbing.NewHash(strings.Repeat("a", 40))
	sha256 := plumbing.NewHash(strings.Repeat("a", 64))
	sha1Object := &countingObject{size: 1}
	sha256Object := &countingObject{size: 1}

	cache.PutWithHash(sha1, sha1Object)
	cache.PutWithHash(sha256, sha256Object)

	got, ok := cache.Get(sha1)
	s.True(ok)
	s.Same(sha1Object, got)
	got, ok = cache.Get(sha256)
	s.True(ok)
	s.Same(sha256Object, got)
}

func (s *ObjectSuite) TestShardedPutGetAndClear() {
	cache := NewShardedObjectLRU(4*Byte, 2)
	first := newObject("00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2*Byte)
	second := newObject("01bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 2*Byte)
	cache.Put(first)
	cache.Put(second)
	got, ok := cache.Get(first.Hash())
	s.True(ok)
	s.Same(first, got)
	got, ok = cache.Get(second.Hash())
	s.True(ok)
	s.Same(second, got)

	cache.Clear()
	_, ok = cache.Get(first.Hash())
	s.False(ok)
	_, ok = cache.Get(second.Hash())
	s.False(ok)
}

func (s *ObjectSuite) TestShardedPutWithHashDoesNotComputeHash() {
	cache := NewShardedObjectLRU(2*Byte, 2)
	want := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	object := &countingObject{size: 1}
	cache.PutWithHash(want, object)
	got, ok := cache.Get(want)
	s.True(ok)
	s.Same(object, got)
	s.Zero(object.hashCalls)
}

func (s *ObjectSuite) TestShardedRejectsObjectLargerThanShard() {
	cache := NewShardedObjectLRU(4*Byte, 2)
	object := newObject("00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 3*Byte)
	cache.Put(object)
	_, ok := cache.Get(object.Hash())
	s.False(ok)
}

func (s *ObjectSuite) TestPutSameObjectWithDifferentSize() {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cache := NewObjectLRU(7 * Byte)
	cache.Put(newObject(hash, 1*Byte))
	cache.Put(newObject(hash, 3*Byte))
	cache.Put(newObject(hash, 5*Byte))
	cache.Put(newObject(hash, 7*Byte))

	s.Equal(7*Byte, cache.MaxSize)
	s.Equal(7*Byte, cache.actualSize)
	s.Equal(1, cache.ll.Len())

	obj, ok := cache.Get(plumbing.NewHash(hash))
	s.Equal(plumbing.NewHash(hash), obj.Hash())
	s.Equal(7*Byte, FileSize(obj.Size()))
	s.True(ok)
}

func (s *ObjectSuite) TestPutBigObject() {
	for _, o := range s.c {
		o.Put(s.bObject)
		_, ok := o.Get(s.aObject.Hash())
		s.False(ok)
	}
}

func (s *ObjectSuite) TestPutCacheOverflow() {
	// this test only works with an specific size
	o := s.c["two_bytes"]

	o.Put(s.aObject)
	o.Put(s.cObject)
	o.Put(s.dObject)

	obj, ok := o.Get(s.aObject.Hash())
	s.False(ok)
	s.Nil(obj)
	obj, ok = o.Get(s.cObject.Hash())
	s.True(ok)
	s.NotNil(obj)
	obj, ok = o.Get(s.dObject.Hash())
	s.True(ok)
	s.NotNil(obj)
}

func (s *ObjectSuite) TestEvictMultipleObjects() {
	o := s.c["two_bytes"]

	o.Put(s.cObject)
	o.Put(s.dObject) // now cache is full with two objects
	o.Put(s.eObject) // this put should evict all previous objects

	obj, ok := o.Get(s.cObject.Hash())
	s.False(ok)
	s.Nil(obj)
	obj, ok = o.Get(s.dObject.Hash())
	s.False(ok)
	s.Nil(obj)
	obj, ok = o.Get(s.eObject.Hash())
	s.True(ok)
	s.NotNil(obj)
}

func (s *ObjectSuite) TestGetUpdatesRecency() {
	cache := NewObjectLRU(2 * Byte)
	cache.Put(s.aObject)
	cache.Put(s.cObject)

	_, ok := cache.Get(s.aObject.Hash())
	s.True(ok)
	cache.Put(s.dObject)

	_, ok = cache.Get(s.aObject.Hash())
	s.True(ok)
	_, ok = cache.Get(s.cObject.Hash())
	s.False(ok)
	_, ok = cache.Get(s.dObject.Hash())
	s.True(ok)
}

func (s *ObjectSuite) TestClear() {
	for _, o := range s.c {
		o.Put(s.aObject)
		o.Clear()
		obj, ok := o.Get(s.aObject.Hash())
		s.False(ok)
		s.Nil(obj)
	}
}

func (s *ObjectSuite) TestConcurrentAccess() {
	for _, o := range s.c {
		var wg sync.WaitGroup

		for i := range 1000 {
			wg.Add(3)
			go func(i int) {
				o.Put(newObject(fmt.Sprint(i), FileSize(i)))
				wg.Done()
			}(i)

			go func(i int) {
				if i%30 == 0 {
					o.Clear()
				}
				wg.Done()
			}(i)

			go func(i int) {
				o.Get(plumbing.NewHash(fmt.Sprint(i)))
				wg.Done()
			}(i)
		}

		wg.Wait()
	}
}

func (s *ObjectSuite) TestDefaultLRU() {
	defaultLRU := s.c["default_lru"].(*ObjectLRU)

	s.Equal(DefaultMaxSize, defaultLRU.MaxSize)
}

func (s *ObjectSuite) TestObjectUpdateOverflow() {
	o := NewObjectLRU(9 * Byte)

	a1 := newObject(s.aObject.Hash().String(), 9*Byte)
	a2 := newObject(s.aObject.Hash().String(), 1*Byte)
	b := newObject(s.bObject.Hash().String(), 1*Byte)

	o.Put(a1)
	a1.SetSize(-5)
	o.Put(a2)
	o.Put(b)
}

func BenchmarkObjectLRUParallelGet(b *testing.B) {
	const objectCount = 4096
	objects := make([]plumbing.EncodedObject, objectCount)
	for i := range objects {
		objects[i] = newObject(fmt.Sprintf("%02x%038x", i%256, i+1), 1*Byte)
	}
	for _, setup := range []struct {
		name  string
		cache Object
	}{
		{name: "single", cache: NewObjectLRU(objectCount * Byte)},
		{name: "sharded-8", cache: NewShardedObjectLRU(objectCount*Byte, 8)},
	} {
		b.Run(setup.name, func(b *testing.B) {
			for _, object := range objects {
				setup.cache.Put(object)
			}
			var seed atomic.Uint64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				next := seed.Add(1)
				for pb.Next() {
					object := objects[next%objectCount]
					if _, ok := setup.cache.Get(object.Hash()); !ok {
						b.Fatal("object was evicted")
					}
					next++
				}
			})
		})
	}
}

type dummyObject struct {
	hash plumbing.Hash
	size FileSize
}

type countingObject struct {
	hashCalls int
	size      int64
}

func (o *countingObject) Hash() plumbing.Hash {
	o.hashCalls++
	return plumbing.ZeroHash
}
func (*countingObject) Type() plumbing.ObjectType       { return plumbing.InvalidObject }
func (*countingObject) SetType(plumbing.ObjectType)     {}
func (o *countingObject) Size() int64                   { return o.size }
func (o *countingObject) SetSize(size int64)            { o.size = size }
func (*countingObject) Reader() (io.ReadCloser, error)  { return nil, nil }
func (*countingObject) Writer() (io.WriteCloser, error) { return nil, nil }

func newObject(hash string, size FileSize) plumbing.EncodedObject {
	return &dummyObject{
		hash: plumbing.NewHash(hash),
		size: size,
	}
}

func (d *dummyObject) Hash() plumbing.Hash           { return d.hash }
func (*dummyObject) Type() plumbing.ObjectType       { return plumbing.InvalidObject }
func (*dummyObject) SetType(plumbing.ObjectType)     {}
func (d *dummyObject) Size() int64                   { return int64(d.size) }
func (d *dummyObject) SetSize(s int64)               { d.size = FileSize(s) }
func (*dummyObject) Reader() (io.ReadCloser, error)  { return nil, nil }
func (*dummyObject) Writer() (io.WriteCloser, error) { return nil, nil }
