-- 0001_init.sql: complete v1 schema for boobies-media.
-- Timestamps are RFC3339 UTC strings. Booleans are INTEGER 0/1.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    display_name  TEXT    NOT NULL,
    avatar_hash   TEXT,
    password_hash TEXT    NOT NULL,
    api_key_hash  TEXT    UNIQUE,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL
);

CREATE TABLE folders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    UNIQUE (parent_id, name)
);

-- UNIQUE(parent_id, name) does NOT constrain root folders, because SQLite
-- treats NULLs as distinct. This partial index closes that hole.
CREATE UNIQUE INDEX folders_root_name ON folders(name) WHERE parent_id IS NULL;

CREATE TABLE jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    type            TEXT    NOT NULL CHECK (type IN ('ingest_url','thumbnail','probe')),
    payload         TEXT    NOT NULL DEFAULT '{}',
    status          TEXT    NOT NULL DEFAULT 'queued'
                            CHECK (status IN ('queued','running','done','failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT    NOT NULL,
    error           TEXT,
    created_at      TEXT    NOT NULL
);

-- Workers poll WHERE status='queued' AND next_attempt_at <= now.
CREATE INDEX jobs_poll ON jobs(status, next_attempt_at);

CREATE TABLE items (
    id            TEXT    PRIMARY KEY,          -- random 8-char base58; also the public share slug
    content_hash  TEXT    NOT NULL,             -- SHA-256 hex of the stored bytes
    title         TEXT    NOT NULL DEFAULT '',
    ext           TEXT    NOT NULL DEFAULT '',
    mime          TEXT    NOT NULL,             -- always from the served-mime allowlist
    size          INTEGER NOT NULL DEFAULT 0,
    width         INTEGER,
    height        INTEGER,
    duration      REAL,
    uploader_id   INTEGER NOT NULL REFERENCES users(id),
    folder_id     INTEGER REFERENCES folders(id) ON DELETE SET NULL,
    source_url    TEXT,
    job_id        INTEGER REFERENCES jobs(id) ON DELETE SET NULL,
    share_revoked INTEGER NOT NULL DEFAULT 0,
    deleted_at    TEXT,                          -- soft delete; admin purge removes the row
    created_at    TEXT    NOT NULL
);

CREATE INDEX items_content_hash   ON items(content_hash);
CREATE INDEX items_folder_created ON items(folder_id, created_at);
CREATE INDEX items_uploader       ON items(uploader_id);
CREATE INDEX items_created        ON items(created_at);

CREATE TABLE tags (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE              -- always stored lowercased
);

CREATE TABLE item_tags (
    item_id TEXT    NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, tag_id)
);

CREATE INDEX item_tags_tag ON item_tags(tag_id, item_id);

CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,           -- SHA-256 hex; the cookie holds the plaintext
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);

CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
