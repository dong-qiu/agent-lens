-- ADR 0014: per-event idempotency key, decoupled from id / the hash chain.
-- `id` stays the server-assigned ordering + chain anchor; `idempotency_key`
-- is the producer-supplied dedup axis that survives a fallback replay.
ALTER TABLE events ADD COLUMN idempotency_key TEXT;

-- Partial unique index: only non-NULL keys are constrained, so pre-0014
-- events (key NULL) and keyless webhook deliveries coexist without
-- conflicting. ON CONFLICT inference in AppendEvent targets this index via
-- the matching `WHERE idempotency_key IS NOT NULL` predicate.
CREATE UNIQUE INDEX events_idempotency_key_uidx
    ON events (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
