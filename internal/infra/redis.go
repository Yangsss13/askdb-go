package infra

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// RedisClient wraps the go-redis client.
type RedisClient struct {
	Client *redis.Client
}

// NewRedis creates a Redis client and verifies connectivity with a Ping.
func NewRedis(addr, password string) (*RedisClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping %s: %w", addr, err)
	}

	slog.Info("redis: connected", "addr", addr)
	return &RedisClient{Client: rdb}, nil
}

// Close releases the Redis connection.
func (r *RedisClient) Close() error {
	return r.Client.Close()
}

// Ping checks the Redis connection.
func (r *RedisClient) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}
