-- In-flight chunked uploads. Rows here are transient: the janitor deletes any
-- row past expires_at along with its temp_dir, so an abandoned upload cannot
-- hold disk forever.
CREATE TABLE uploads (
    id            TEXT PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id     INTEGER REFERENCES folders(id) ON DELETE SET NULL,
    filename      TEXT NOT NULL,
    declared_size INTEGER NOT NULL,
    chunk_size    INTEGER NOT NULL,
    received      TEXT NOT NULL DEFAULT '[]',
    temp_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL
);

CREATE INDEX uploads_expires_at ON uploads(expires_at);
CREATE INDEX uploads_user_id ON uploads(user_id);
