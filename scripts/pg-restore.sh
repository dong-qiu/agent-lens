#!/usr/bin/env bash
# pg-restore.sh — restore an agent-lens Postgres dump produced by
# pg-backup.sh into a target database.
#
# Usage:
#   scripts/pg-restore.sh <dump_file> [target_dsn]
#
# Defaults target_dsn to PG_DSN env var, falling back to the dev DSN
# baked into the Makefile.
#
# Refuses to restore into a non-empty target unless TARGET_OVERWRITE=1
# is set: the events table is the integrity-critical artefact and we
# would rather error than silently merge two histories.
#
# If a sidecar <dump>.sha256 exists, the digest is verified before
# touching the target.
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <dump_file> [target_dsn]" >&2
  exit 2
fi

dump="$1"
target_dsn="${2:-${PG_DSN:-postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable}}"

if [[ ! -r "$dump" ]]; then
  echo "dump file not readable: $dump" >&2
  exit 1
fi

# Integrity check (best-effort — not all environments have sha256sum).
# Compare hashes directly rather than `-c` so we don't care what path
# the sidecar recorded (absolute / relative / basename — any survive).
if [[ -f "$dump.sha256" ]]; then
  hash_cmd="$(command -v sha256sum || command -v shasum)"
  if [[ -n "$hash_cmd" ]]; then
    expected="$(awk '{print $1}' "$dump.sha256")"
    if [[ "$hash_cmd" == *shasum ]]; then
      actual="$("$hash_cmd" -a 256 "$dump" | awk '{print $1}')"
    else
      actual="$("$hash_cmd" "$dump" | awk '{print $1}')"
    fi
    if [[ -z "$expected" || "$expected" != "$actual" ]]; then
      echo "sha256 mismatch: sidecar=$expected actual=$actual" >&2
      exit 1
    fi
    echo "→ sha256 ok ($expected)"
  fi
fi

if ! command -v psql >/dev/null 2>&1 || ! command -v pg_restore >/dev/null 2>&1; then
  echo "pg_restore / psql not found in PATH. Install postgresql-client." >&2
  exit 1
fi

# Refuse to clobber a non-empty target. Hash chain integrity hinges on
# events being a single authoritative log; merging two restores would
# produce a corrupted chain.
#
# Use to_regclass + an actual COUNT against each table that exists.
# pg_stat_user_tables.n_live_tup is a stats estimate that can lag bulk
# inserts (or be 0 right after a restore before autovacuum runs), so
# trusting it would let the safety gate slip past a freshly populated
# target.
count_sql="SELECT
  COALESCE((CASE WHEN to_regclass('public.events')    IS NOT NULL THEN (SELECT COUNT(*) FROM events)    ELSE 0 END), 0)
+ COALESCE((CASE WHEN to_regclass('public.links')     IS NOT NULL THEN (SELECT COUNT(*) FROM links)     ELSE 0 END), 0)
+ COALESCE((CASE WHEN to_regclass('public.artifacts') IS NOT NULL THEN (SELECT COUNT(*) FROM artifacts) ELSE 0 END), 0)"
existing_count="$(psql "$target_dsn" -tAc "$count_sql" 2>/dev/null || echo 0)"
existing_count="$(echo "$existing_count" | tr -d '[:space:]')"
if [[ -n "$existing_count" && "$existing_count" != "0" && "${TARGET_OVERWRITE:-}" != "1" ]]; then
  echo "target already contains $existing_count rows in events/links/artifacts." >&2
  echo "Refusing to restore into a non-empty database." >&2
  echo "If you intend to overwrite, set TARGET_OVERWRITE=1 and re-run." >&2
  exit 1
fi

echo "→ restoring $dump → $target_dsn"
# --clean drops existing matching objects before recreating; --if-exists
# avoids errors when the target was already empty. --no-owner /
# --no-privileges so the dump's role assumptions don't break a
# differently-owned target DB.
pg_restore \
  --dbname="$target_dsn" \
  --clean --if-exists \
  --no-owner --no-privileges \
  --exit-on-error \
  "$dump"

echo "✓ restored from $dump"

# Quick sanity probe: confirm the restored events have a non-empty
# hash chain head somewhere. Doesn't substitute for full verify, just
# catches "we restored an empty dump" type mistakes.
restored_events="$(psql "$target_dsn" -tAc "SELECT COUNT(*) FROM events" | tr -d '[:space:]')"
echo "→ restored events: $restored_events"
echo "  Run scripts/verify-backup-integrity.sh against a known session id"
echo "  to confirm hash chain is intact end-to-end."
