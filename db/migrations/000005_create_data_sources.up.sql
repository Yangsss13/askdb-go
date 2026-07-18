CREATE TABLE data_sources (
  id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id              BIGINT UNSIGNED NOT NULL
                         COMMENT 'Owner; restricted to their own rows at the application layer',
  label                VARCHAR(100)    NOT NULL
                         COMMENT 'Human-readable name; unique per user including soft-deleted rows',
  host                 VARCHAR(253)    NOT NULL,
  port                 SMALLINT UNSIGNED NOT NULL,
  database_name        VARCHAR(64)     NOT NULL,
  username             VARCHAR(64)     NOT NULL,
  -- password_ciphertext stores "v1:<base64(nonce||ciphertext)>" — never log or return this column.
  password_ciphertext  TEXT            NOT NULL,
  -- tls_mode: 'disabled' or 'verify-full' only; verify-full is the default.
  tls_mode             VARCHAR(16)     NOT NULL DEFAULT 'verify-full',
  connect_timeout_sec  TINYINT UNSIGNED NOT NULL DEFAULT 5,
  created_at           DATETIME(3)     NOT NULL,
  updated_at           DATETIME(3)     NOT NULL,
  -- Soft-delete: deleted_at IS NOT NULL means the row is logically deleted.
  -- The label occupancy is intentional: a deleted source's label stays reserved
  -- so existing query_jobs referencing it can still be identified by label.
  deleted_at           DATETIME(3)     NULL,

  PRIMARY KEY (id),
  INDEX idx_data_sources_user_id (user_id),
  -- Unique label per user across ALL rows (including soft-deleted) so that
  -- re-creating a source with the same name requires explicit cleanup.
  UNIQUE KEY uq_data_sources_user_label (user_id, label),
  CONSTRAINT fk_data_sources_user_id
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
