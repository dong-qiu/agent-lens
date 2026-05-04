-- Append-only event log. Order is established by (session_id, ts) and the
-- hash chain (prev_hash -> hash). Application code refuses UPDATE/DELETE.
CREATE TABLE events (
    id           TEXT PRIMARY KEY,            -- ULID
    ts           TIMESTAMPTZ NOT NULL,
    session_id   TEXT NOT NULL,
    turn_id      TEXT,
    actor_type   TEXT NOT NULL,
    actor_id     TEXT NOT NULL,
    actor_model  TEXT,
    kind         TEXT NOT NULL,
    payload      JSONB,
    parents      TEXT[] NOT NULL DEFAULT '{}',
    refs         TEXT[] NOT NULL DEFAULT '{}',
    hash         TEXT NOT NULL,
    prev_hash    TEXT,
    sig          BYTEA
);

CREATE INDEX events_session_ts_idx ON events (session_id, ts);
CREATE INDEX events_kind_ts_idx    ON events (kind, ts DESC);
CREATE INDEX events_turn_idx       ON events (turn_id) WHERE turn_id IS NOT NULL;

-- Content-addressed artifact metadata. The blob itself lives in object
-- storage (MinIO/S3); this table records type and references.
CREATE TABLE artifacts (
    id        TEXT PRIMARY KEY,               -- sha256 hex
    type      TEXT NOT NULL,
    meta      JSONB,
    blob_ref  TEXT NOT NULL
);

-- Causal/semantic relations between events. Kept separate from the event
-- log so linking can be re-derived without rewriting append-only history.
CREATE TABLE links (
    from_event   TEXT NOT NULL REFERENCES events(id),
    to_event     TEXT NOT NULL REFERENCES events(id),
    relation     TEXT NOT NULL,
    confidence   REAL NOT NULL DEFAULT 1.0,
    inferred_by  TEXT NOT NULL,
    PRIMARY KEY (from_event, to_event, relation)
);

CREATE INDEX links_to_idx   ON links (to_event);
CREATE INDEX links_from_idx ON links (from_event);
