-- =============================================================
-- AskDB demo seed
-- This file is executed automatically by the MySQL Docker
-- container on first start (mounted as an init script).
--
-- Two databases are created:
--   askdb_app   — application data (managed by GORM)
--   askdb_demo  — read-only demo data for NL-to-SQL demonstrations
--
-- Two restricted users are created:
--   askdb_app    — full access to askdb_app only (used by the Go app)
--   askdb_reader — SELECT-only access to askdb_demo (used for dynamic queries)
-- =============================================================

-- ---------------------------------------------------------------
-- Databases
-- ---------------------------------------------------------------
CREATE DATABASE IF NOT EXISTS askdb_app
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

CREATE DATABASE IF NOT EXISTS askdb_demo
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

-- ---------------------------------------------------------------
-- Users (dev-only passwords, never use in production)
-- ---------------------------------------------------------------
CREATE USER IF NOT EXISTS 'askdb_app'@'%'   IDENTIFIED BY 'app_dev_pass';
CREATE USER IF NOT EXISTS 'askdb_reader'@'%' IDENTIFIED BY 'reader_dev_pass';

GRANT ALL PRIVILEGES        ON askdb_app.* TO 'askdb_app'@'%';
GRANT SELECT                ON askdb_demo.* TO 'askdb_reader'@'%';
FLUSH PRIVILEGES;

-- ---------------------------------------------------------------
-- askdb_demo schema
-- ---------------------------------------------------------------
USE askdb_demo;

CREATE TABLE IF NOT EXISTS products (
  id          INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  name        VARCHAR(120)    NOT NULL,
  category    VARCHAR(60)     NOT NULL,
  price       DECIMAL(10, 2)  NOT NULL,
  stock       INT UNSIGNED    NOT NULL DEFAULT 0,
  created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS orders (
  id           INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  customer     VARCHAR(80)    NOT NULL,
  status       ENUM('pending','shipped','delivered','cancelled') NOT NULL DEFAULT 'pending',
  total_amount DECIMAL(10, 2) NOT NULL DEFAULT 0.00,
  created_at   DATETIME       NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS order_items (
  id          INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  order_id    INT UNSIGNED    NOT NULL,
  product_id  INT UNSIGNED    NOT NULL,
  quantity    INT UNSIGNED    NOT NULL,
  unit_price  DECIMAL(10, 2)  NOT NULL,
  FOREIGN KEY (order_id)   REFERENCES orders(id),
  FOREIGN KEY (product_id) REFERENCES products(id)
) ENGINE=InnoDB;

-- ---------------------------------------------------------------
-- askdb_demo seed data
-- 10 products, 5 orders, 15 order_items
-- ---------------------------------------------------------------
INSERT INTO products (id, name, category, price, stock) VALUES
  (1,  'Wireless Mouse',          'Electronics',  29.99,  150),
  (2,  'Mechanical Keyboard',     'Electronics',  89.99,   80),
  (3,  'USB-C Hub 7-in-1',        'Electronics',  45.00,  200),
  (4,  'Monitor Stand',           'Furniture',    35.50,   60),
  (5,  'Laptop Backpack 15"',     'Bags',         59.99,  120),
  (6,  'Noise-Cancelling Headphones', 'Electronics', 149.00, 45),
  (7,  'Standing Desk Mat',       'Furniture',    39.99,   90),
  (8,  'Webcam 1080p',            'Electronics',  69.00,   55),
  (9,  'HDMI Cable 2m',           'Accessories',   9.99,  300),
  (10, 'Desk Lamp LED',           'Furniture',    24.99,  110);

INSERT INTO orders (id, customer, status, total_amount, created_at) VALUES
  (1, 'Alice Wang',  'delivered', 179.97, '2024-01-10 09:15:00'),
  (2, 'Bob Chen',    'shipped',   149.00, '2024-01-12 14:30:00'),
  (3, 'Carol Liu',   'pending',   134.98, '2024-01-14 10:00:00'),
  (4, 'David Zhang', 'cancelled',  29.99, '2024-01-15 16:45:00'),
  (5, 'Emma Sun',    'delivered', 198.97, '2024-01-18 11:20:00');

INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES
  -- Order 1: Alice Wang — mouse + keyboard + HDMI cable
  (1, 1, 1,  29.99),
  (1, 2, 1,  89.99),
  (1, 9, 6,   9.99),
  -- Order 2: Bob Chen — headphones only
  (2, 6, 1, 149.00),
  -- Order 3: Carol Liu — USB-C hub + monitor stand + HDMI cable
  (3, 3, 2,  45.00),
  (3, 4, 1,  35.50),
  (3, 9, 1,   9.99),
  -- Order 4: David Zhang — mouse (cancelled)
  (4, 1, 1,  29.99),
  -- Order 5: Emma Sun — keyboard + webcam + desk lamp
  (5, 2, 1,  89.99),
  (5, 8, 1,  69.00),
  (5, 10, 1, 24.99),
  -- Additional items to reach 15
  (1, 7, 1,  39.99),
  (3, 7, 1,  39.99),
  (5, 3, 1,  45.00),
  (2, 9, 2,   9.99);
