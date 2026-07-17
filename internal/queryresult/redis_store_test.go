package queryresult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeRedis is a hand-written fake of redisCommands for unit tests.
type fakeRedis struct {
	setKey string
	setVal []byte
	setTTL time.Duration
	setErr error

	getVal []byte
	getErr error
}

func (f *fakeRedis) Set(_ context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	f.setKey = key
	if b, ok := value.([]byte); ok {
		f.setVal = b
	}
	f.setTTL = expiration
	cmd := redis.NewStatusCmd(context.Background())
	if f.setErr != nil {
		cmd.SetErr(f.setErr)
	}
	return cmd
}

func (f *fakeRedis) Get(_ context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	if f.getErr != nil {
		cmd.SetErr(f.getErr)
		return cmd
	}
	cmd.SetVal(string(f.getVal))
	return cmd
}

func sampleResult(jobID uint64) CachedQueryResult {
	now := time.Now().UTC()
	return CachedQueryResult{
		JobID:     jobID,
		Columns:   []string{"id", "name"},
		Rows:      [][]any{{int64(1), "商品A"}},
		RowCount:  1,
		CachedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
}

func TestRedisStore_Set_CorrectKeyAndTTL(t *testing.T) {
	fake := &fakeRedis{}
	store := &RedisStore{rdb: fake}

	result := sampleResult(42)
	ttl := 15 * time.Minute

	if err := store.Set(context.Background(), result, ttl); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantKey := "askdb:query-result:42:v1"
	if fake.setKey != wantKey {
		t.Errorf("Set key: got %q, want %q", fake.setKey, wantKey)
	}
	if fake.setTTL != ttl {
		t.Errorf("Set TTL: got %v, want %v", fake.setTTL, ttl)
	}
	if len(fake.setVal) == 0 {
		t.Error("Set value must not be empty")
	}
}

func TestRedisStore_Get_Success(t *testing.T) {
	result := sampleResult(7)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	fake := &fakeRedis{getVal: encoded}
	store := &RedisStore{rdb: fake}

	got, err := store.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.JobID != 7 {
		t.Errorf("JobID: got %d, want 7", got.JobID)
	}
	if got.RowCount != 1 {
		t.Errorf("RowCount: got %d, want 1", got.RowCount)
	}
	if len(got.Columns) != 2 {
		t.Errorf("Columns len: got %d, want 2", len(got.Columns))
	}
	// Verify int64 is preserved after round-trip through Redis fake.
	if v, ok := got.Rows[0][0].(int64); !ok || v != 1 {
		t.Errorf("int64 preserved: got %T(%v), want int64(1)", got.Rows[0][0], got.Rows[0][0])
	}
}

func TestRedisStore_Get_KeyNotFound(t *testing.T) {
	fake := &fakeRedis{getErr: redis.Nil}
	store := &RedisStore{rdb: fake}

	_, err := store.Get(context.Background(), 99)
	if !errors.Is(err, ErrResultNotFound) {
		t.Errorf("expected ErrResultNotFound, got %v", err)
	}
}

func TestRedisStore_Get_CorruptedJSON(t *testing.T) {
	fake := &fakeRedis{getVal: []byte("not valid json {")}
	store := &RedisStore{rdb: fake}

	_, err := store.Get(context.Background(), 1)
	if !errors.Is(err, ErrResultCorrupted) {
		t.Errorf("expected ErrResultCorrupted, got %v", err)
	}
}

func TestRedisStore_Get_StoreUnavailable(t *testing.T) {
	fake := &fakeRedis{getErr: fmt.Errorf("dial tcp: connection refused")}
	store := &RedisStore{rdb: fake}

	_, err := store.Get(context.Background(), 1)
	if !errors.Is(err, ErrResultStoreUnavailable) {
		t.Errorf("expected ErrResultStoreUnavailable, got %v", err)
	}
}

func TestRedisStore_Set_PropagatesError(t *testing.T) {
	fake := &fakeRedis{setErr: fmt.Errorf("redis: write error")}
	store := &RedisStore{rdb: fake}

	err := store.Set(context.Background(), sampleResult(1), time.Minute)
	if !errors.Is(err, ErrResultStoreUnavailable) {
		t.Errorf("expected ErrResultStoreUnavailable on Set error, got %v", err)
	}
}
