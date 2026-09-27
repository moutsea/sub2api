package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type kiroThinkingCache struct{ rdb *redis.Client }

func NewKiroThinkingCache(rdb *redis.Client) service.KiroThinkingCache {
	return &kiroThinkingCache{rdb: rdb}
}

func (c *kiroThinkingCache) Put(ctx context.Context, key string, content []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, content, ttl).Err()
}

func (c *kiroThinkingCache) Get(ctx context.Context, key string) ([]byte, error) {
	content, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return content, err
}
