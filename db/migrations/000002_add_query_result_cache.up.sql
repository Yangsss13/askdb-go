-- Add result_expires_at to query_jobs.
-- Records when the Redis-cached query result expires.
-- NULL means the job has not succeeded yet, or succeeded before caching was introduced.
ALTER TABLE query_jobs
    ADD COLUMN result_expires_at DATETIME(3) NULL
        COMMENT 'Redis cache expiry time; NULL when job is not yet succeeded or cache was not written';
