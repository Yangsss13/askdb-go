package queryresult

import "errors"

// Sentinel errors returned by RedisStore. Callers use errors.Is to distinguish them.
var (
	// ErrResultNotFound is returned when the Redis key does not exist (redis.Nil).
	ErrResultNotFound = errors.New("queryresult: not found")

	// ErrResultCorrupted is returned when the stored JSON cannot be decoded.
	ErrResultCorrupted = errors.New("queryresult: corrupted data")

	// ErrResultStoreUnavailable is returned when Redis is unreachable or returns
	// an unexpected command error (not redis.Nil).
	ErrResultStoreUnavailable = errors.New("queryresult: store unavailable")
)
