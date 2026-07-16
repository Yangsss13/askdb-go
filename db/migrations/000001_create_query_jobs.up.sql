-- Create the query_jobs table in askdb_app.
-- Tracks each natural-language query submission and its final outcome.
CREATE TABLE IF NOT EXISTS query_jobs (
  id                     BIGINT UNSIGNED  AUTO_INCREMENT PRIMARY KEY,
  question               VARCHAR(500)     NOT NULL,
  generated_sql          TEXT             NULL,
  status                 VARCHAR(20)      NOT NULL,
  error_code             VARCHAR(40)      NULL,
  error_message          VARCHAR(255)     NULL,
  row_count              INT UNSIGNED     NULL,
  execution_duration_ms  BIGINT UNSIGNED  NULL,
  created_at             DATETIME(3)      NOT NULL,
  updated_at             DATETIME(3)      NOT NULL,
  finished_at            DATETIME(3)      NULL,
  INDEX idx_query_jobs_status (status),
  INDEX idx_query_jobs_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
