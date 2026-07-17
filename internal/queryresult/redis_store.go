package queryresult

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Yangsss13/askdb-go/internal/infra"
)

// redisCommands is the minimal Redis interface RedisStore needs.
// *redis.Client satisfies this; tests supply a hand-written fake.
type redisCommands interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
}

// RedisStore persists and retrieves CachedQueryResult values in Redis.
type RedisStore struct {
	rdb redisCommands
}

// NewRedisStore returns a store backed by the given infra.RedisClient.
func NewRedisStore(rdb *infra.RedisClient) *RedisStore {
	return &RedisStore{rdb: rdb.Client}
}

// Marshal serializes a CachedQueryResult to its JSON payload. Callers that need
// to enforce a size limit on the exact bytes written to Redis can marshal once
// with this function and pass the result to SetRaw.
func Marshal(result CachedQueryResult) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("queryresult: marshal: %w", err)
	}
	return data, nil
}

// Set serializes result as JSON and writes it to Redis under the job's key
// with the given TTL. TTL must be greater than zero.
func (s *RedisStore) Set(ctx context.Context, result CachedQueryResult, ttl time.Duration) error {
	data, err := Marshal(result)
	if err != nil {
		return err
	}
	return s.SetRaw(ctx, result.JobID, data, ttl)
}

// SetRaw writes an already-serialized payload to Redis under the job's key with
// the given TTL. TTL must be greater than zero.
func (s *RedisStore) SetRaw(ctx context.Context, jobID uint64, payload []byte, ttl time.Duration) error {
	key := QueryResultKey(jobID)
	if err := s.rdb.Set(ctx, key, payload, ttl).Err(); err != nil {
		return ErrResultStoreUnavailable
	}
	return nil
}

// Get retrieves and deserializes the cached result for the given job ID.
// Returns ErrResultNotFound when the key is absent, ErrResultCorrupted when
// the stored data cannot be decoded, and ErrResultStoreUnavailable on Redis
// connection or command errors.
func (s *RedisStore) Get(ctx context.Context, jobID uint64) (*CachedQueryResult, error) {
	key := QueryResultKey(jobID)
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrResultNotFound
		}
		return nil, ErrResultStoreUnavailable
	}

	result, err := decodeCachedResult(data)
	if err != nil {
		return nil, ErrResultCorrupted
	}
	return result, nil
}

// decodeCachedResult decodes JSON using UseNumber so that integer values in
// rows are preserved as int64 rather than silently promoted to float64.
func decodeCachedResult(data []byte) (*CachedQueryResult, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var result CachedQueryResult
	if err := dec.Decode(&result); err != nil {
		return nil, err
	}

	// Normalize json.Number values inside rows so callers receive Go-native
	// types: integers as int64, decimals-with-fraction as float64.
	for i, row := range result.Rows {
		for j, cell := range row {
			if n, ok := cell.(json.Number); ok {
				result.Rows[i][j] = normalizeNumber(n)
			}
		}
	}
	return &result, nil
}

// normalizeNumber converts a json.Number to int64 when the string contains no
// decimal point and fits in int64, otherwise falls back to float64.
// This preserves the original types produced by QueryExecutor (int64 for
// integer columns, float64 for floating-point columns).
func normalizeNumber(n json.Number) any {
	s := n.String()
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	// Last resort: keep as string to avoid data loss.
	return s
}
