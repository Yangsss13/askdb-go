ALTER TABLE query_jobs
  DROP FOREIGN KEY fk_query_jobs_data_source_id,
  DROP INDEX idx_query_jobs_data_source_id,
  DROP COLUMN data_source_id;
