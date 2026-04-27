-- GIN index on the refs array makes EventsByRef (used by the linking
-- worker to find peers via shared refs like "git:<sha>") a sub-ms
-- lookup instead of a full table scan.
CREATE INDEX IF NOT EXISTS events_refs_gin_idx ON events USING GIN (refs);
