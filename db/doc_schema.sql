-- doc-service owns documents, membership, the append-only operation log and the
-- outbox. owner_id / user_id columns deliberately have NO foreign key to
-- docs_gateway_db.users: the two services are independently deployable and MySQL
-- cannot enforce integrity across schemas owned by different deployables. The id
-- is trusted because the gateway validated the token it came from.
--
-- This file is self-contained (CREATE DATABASE + USE + IF NOT EXISTS) so it can be
-- applied to a live volume as well as run as a docker-entrypoint-initdb.d script,
-- which only fires when the data directory is empty:
--   docker exec -i docs_mysql mysql -uroot -proot < db/doc_schema.sql

CREATE DATABASE IF NOT EXISTS docs_service_db;

USE docs_service_db;

-- DATETIME(3) rather than DATETIME: second-resolution ties make LIMIT/OFFSET
-- pagination non-deterministic. Queries order by updated_at DESC, id DESC.
CREATE TABLE IF NOT EXISTS documents (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    owner_id    BIGINT NOT NULL,                 -- no FK: other schema, other deployable
    title       VARCHAR(255) NOT NULL,
    content     MEDIUMTEXT NOT NULL,             -- snapshot of the text at `version`
    version     BIGINT NOT NULL DEFAULT 0,       -- number of ops folded into `content`
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    INDEX idx_documents_owner (owner_id, updated_at)
);

-- Append-only. Version v is the op that takes the document from state v-1 to v.
-- The primary key is the ordering guarantee: two instances that ever tried to
-- write version 42 for the same document would collide here, which is the
-- database-level backstop behind sticky routing (month 3).
CREATE TABLE IF NOT EXISTS document_ops (
    doc_id      BIGINT NOT NULL,
    version     BIGINT NOT NULL,
    user_id     BIGINT NOT NULL,
    op          JSON NOT NULL,                   -- [retain, "insert", -delete, ...]
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (doc_id, version)
);

CREATE TABLE IF NOT EXISTS document_members (
    doc_id      BIGINT NOT NULL,
    user_id     BIGINT NOT NULL,
    role        ENUM('owner','editor','viewer') NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (doc_id, user_id),
    INDEX idx_members_user (user_id)
);
-- The owner is also a row here (role='owner'), so every permission check is one
-- lookup in one table. documents.owner_id is kept for listing and for transfer.

-- Month 5. Written in the SAME transaction as the change it describes
-- (transactional outbox); relayed to notification-service by a poller.
CREATE TABLE IF NOT EXISTS outbox (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_id     CHAR(32) NOT NULL UNIQUE,       -- random hex; the idempotency key downstream
    event_type   VARCHAR(64) NOT NULL,           -- document.shared | document.unshared | document.updated | document.published
    doc_id       BIGINT NOT NULL,
    payload      JSON NOT NULL,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    processed_at DATETIME(3) NULL,
    INDEX idx_outbox_pending (processed_at, id)
);
