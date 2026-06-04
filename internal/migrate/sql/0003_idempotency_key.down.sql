DROP INDEX IF EXISTS events_idempotency_key_uidx;
ALTER TABLE events DROP COLUMN IF EXISTS idempotency_key;
