ALTER TABLE query_jobs
  ADD COLUMN data_source_id BIGINT UNSIGNED NULL
    COMMENT 'NULL = pre-6B legacy rows; application layer enforces non-NULL for new jobs',
  ADD INDEX idx_query_jobs_data_source_id (data_source_id),
  ADD CONSTRAINT fk_query_jobs_data_source_id
    FOREIGN KEY (data_source_id) REFERENCES data_sources(id) ON DELETE RESTRICT;
