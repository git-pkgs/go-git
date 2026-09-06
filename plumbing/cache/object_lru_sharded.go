package cache

import "github.com/go-git/go-git/v6/plumbing"

// ShardedObjectLRU divides an object cache across independent LRU shards.
// An object larger than its shard's share of MaxSize is not cached.
type ShardedObjectLRU struct {
	MaxSize FileSize
	shards  []*ObjectLRU
}

// NewShardedObjectLRU creates a cache with shardCount independent locks.
func NewShardedObjectLRU(maxSize FileSize, shardCount int) *ShardedObjectLRU {
	if shardCount < 1 {
		shardCount = 1
	}
	cache := &ShardedObjectLRU{
		MaxSize: maxSize,
		shards:  make([]*ObjectLRU, shardCount),
	}
	perShard := maxSize / FileSize(shardCount)
	remainder := maxSize % FileSize(shardCount)
	for i := range cache.shards {
		shardSize := perShard
		if FileSize(i) < remainder {
			shardSize++
		}
		cache.shards[i] = NewObjectLRU(shardSize)
	}
	return cache
}

func (c *ShardedObjectLRU) shard(hash plumbing.Hash) *ObjectLRU {
	return c.shards[int(hash.Bytes()[0])%len(c.shards)]
}

// Put inserts an object into the shard selected by its hash.
func (c *ShardedObjectLRU) Put(object plumbing.EncodedObject) {
	c.PutWithHash(object.Hash(), object)
}

// PutWithHash inserts an object whose ID is already known.
func (c *ShardedObjectLRU) PutWithHash(hash plumbing.Hash, object plumbing.EncodedObject) {
	c.shard(hash).PutWithHash(hash, object)
}

// Get returns an object by hash and marks it as used within its shard.
func (c *ShardedObjectLRU) Get(hash plumbing.Hash) (plumbing.EncodedObject, bool) {
	return c.shard(hash).Get(hash)
}

// Clear removes every cached object.
func (c *ShardedObjectLRU) Clear() {
	for _, shard := range c.shards {
		shard.Clear()
	}
}
