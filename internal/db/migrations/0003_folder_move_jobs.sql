-- migrate:foreign-keys-off
CREATE TABLE jobs_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    type            TEXT    NOT NULL CHECK (type IN ('ingest_url','thumbnail','probe','folder_move')),
    payload         TEXT    NOT NULL DEFAULT '{}',
    status          TEXT    NOT NULL DEFAULT 'queued'
                            CHECK (status IN ('queued','running','done','failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT    NOT NULL,
    error           TEXT,
    created_at      TEXT    NOT NULL
);

INSERT INTO jobs_new (id, type, payload, status, attempts, next_attempt_at, error, created_at)
SELECT id, type, payload, status, attempts, next_attempt_at, error, created_at
FROM jobs;

DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE INDEX jobs_poll ON jobs(status, next_attempt_at);
