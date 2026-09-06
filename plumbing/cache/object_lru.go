package cache

import (
	"sync"

	"github.com/go-git/go-git/v6/plumbing"
)

// ObjectLRU implements an object cache with an LRU eviction policy and a
// maximum size (measured in object size).
type ObjectLRU struct {
	MaxSize FileSize

	actualSize FileSize
	ll         objectCacheList
	cache      map[plumbing.Hash]*objectCacheEntry
	free       *objectCacheEntry
	mut        sync.Mutex
}

type objectCacheEntry struct {
	hash   plumbing.Hash
	object plumbing.EncodedObject
	prev   *objectCacheEntry
	next   *objectCacheEntry
}

type objectCacheList struct {
	front *objectCacheEntry
	back  *objectCacheEntry
	len   int
}

func (l *objectCacheList) Len() int {
	return l.len
}

func (l *objectCacheList) PushFront(entry *objectCacheEntry) {
	entry.prev = nil
	entry.next = l.front
	if l.front == nil {
		l.back = entry
	} else {
		l.front.prev = entry
	}
	l.front = entry
	l.len++
}

func (l *objectCacheList) MoveToFront(entry *objectCacheEntry) {
	if l.front == entry {
		return
	}
	l.Remove(entry)
	l.PushFront(entry)
}

func (l *objectCacheList) Back() *objectCacheEntry {
	return l.back
}

func (l *objectCacheList) Remove(entry *objectCacheEntry) {
	if entry.prev == nil {
		l.front = entry.next
	} else {
		entry.prev.next = entry.next
	}
	if entry.next == nil {
		l.back = entry.prev
	} else {
		entry.next.prev = entry.prev
	}
	entry.prev = nil
	entry.next = nil
	l.len--
}

// NewObjectLRU creates a new ObjectLRU with the given maximum size. The maximum
// size will never be exceeded.
func NewObjectLRU(maxSize FileSize) *ObjectLRU {
	return &ObjectLRU{MaxSize: maxSize}
}

// NewObjectLRUDefault creates a new ObjectLRU with the default cache size.
func NewObjectLRUDefault() *ObjectLRU {
	return &ObjectLRU{MaxSize: DefaultMaxSize}
}

// Put puts an object into the cache. If the object is already in the cache, it
// will be marked as used. Otherwise, it will be inserted. A single object might
// be evicted to make room for the new object.
func (c *ObjectLRU) Put(obj plumbing.EncodedObject) {
	c.PutWithHash(obj.Hash(), obj)
}

// PutWithHash inserts an object whose ID is already known.
func (c *ObjectLRU) PutWithHash(hash plumbing.Hash, obj plumbing.EncodedObject) {
	c.mut.Lock()
	defer c.mut.Unlock()

	if c.cache == nil {
		c.actualSize = 0
		c.cache = make(map[plumbing.Hash]*objectCacheEntry, 1000)
		c.ll = objectCacheList{}
	}

	objSize := FileSize(obj.Size())
	if ee, ok := c.cache[hash]; ok {
		oldObj := ee.object
		// in this case objSize is a delta: new size - old size
		objSize -= FileSize(oldObj.Size())
		c.ll.MoveToFront(ee)
		ee.object = obj
	} else {
		if objSize > c.MaxSize {
			return
		}
		ee := c.acquireEntry(hash, obj)
		c.ll.PushFront(ee)
		c.cache[hash] = ee
	}

	c.actualSize += objSize
	for c.actualSize > c.MaxSize {
		last := c.ll.Back()
		if last == nil {
			c.actualSize = 0
			break
		}

		lastSize := FileSize(last.object.Size())

		c.ll.Remove(last)
		delete(c.cache, last.hash)
		c.actualSize -= lastSize
		c.releaseEntry(last)
	}
}

func (c *ObjectLRU) acquireEntry(hash plumbing.Hash, object plumbing.EncodedObject) *objectCacheEntry {
	entry := c.free
	if entry == nil {
		return &objectCacheEntry{hash: hash, object: object}
	}
	c.free = entry.next
	entry.hash = hash
	entry.object = object
	entry.next = nil
	return entry
}

func (c *ObjectLRU) releaseEntry(entry *objectCacheEntry) {
	entry.object = nil
	entry.next = c.free
	c.free = entry
}

// Get returns an object by its hash. It marks the object as used. If the object
// is not in the cache, (nil, false) will be returned.
func (c *ObjectLRU) Get(k plumbing.Hash) (plumbing.EncodedObject, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	ee, ok := c.cache[k]
	if !ok {
		return nil, false
	}

	c.ll.MoveToFront(ee)
	return ee.object, true
}

// Clear the content of this object cache.
func (c *ObjectLRU) Clear() {
	c.mut.Lock()
	defer c.mut.Unlock()

	c.ll = objectCacheList{}
	c.cache = nil
	c.free = nil
	c.actualSize = 0
}
