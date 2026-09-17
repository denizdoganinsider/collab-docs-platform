-- gateway owns users and authentication. This is the only schema that holds a
-- users table; every other service trusts the user id the gateway forwards in
-- X-User-ID (validated once, at the edge) and never joins against this table.
--
-- This file is self-contained (CREATE DATABASE + USE + IF NOT EXISTS) so it can be
-- applied to a live volume as well as run as a docker-entrypoint-initdb.d script,
-- which only fires when the data directory is empty:
--   docker exec -i docs_mysql mysql -uroot -proot < db/gateway_schema.sql

CREATE DATABASE IF NOT EXISTS docs_gateway_db;

USE docs_gateway_db;

CREATE TABLE IF NOT EXISTS users (
    id            BIGINT AUTO_INCREMENT PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role          ENUM('user','admin') NOT NULL DEFAULT 'user',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
