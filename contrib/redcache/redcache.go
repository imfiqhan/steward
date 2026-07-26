// Package redcache implements steward.Cache over Redis (go-redis v9) for
// multi-instance deployments where the in-process cache would go stale.
//
//	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
//	admin, _ := steward.New(steward.Config{Cache: redcache.New(rdb), ...})
package redcache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	steward "github.com/imfiqhan/steward"
)

// Cache adapts a Redis client to steward.Cache.
type Cache struct {
	rdb    redis.UniversalClient
	prefix string
}

var _ steward.Cache = (*Cache)(nil)

// Option configures the cache.
type Option func(*Cache)

// WithPrefix namespaces keys (default "steward:").
func WithPrefix(p string) Option { return func(c *Cache) { c.prefix = p } }

// New wraps an existing Redis client.
func New(rdb redis.UniversalClient, opts ...Option) *Cache {
	c := &Cache{rdb: rdb, prefix: "steward:"}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get implements steward.Cache.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.rdb.Get(ctx, c.prefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

// Set implements steward.Cache; ttl <= 0 stores without expiry.
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl < 0 {
		ttl = 0
	}
	return c.rdb.Set(ctx, c.prefix+key, val, ttl).Err()
}

// Delete implements steward.Cache.
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.prefix + k
	}
	return c.rdb.Del(ctx, full...).Err()
}
