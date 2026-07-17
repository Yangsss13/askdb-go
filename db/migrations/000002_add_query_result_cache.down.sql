-- Reverse 000002: remove the result_expires_at column added for Redis caching.
ALTER TABLE query_jobs
    DROP COLUMN result_expires_at;
