#!/usr/bin/env bash
# pg-backup.sh — dump the agent-lens Postgres event store to a
# timestamped custom-format file plus a sha256 sidecar.
#
# Usage:
#   scripts/pg-backup.sh [output_dir]
#
# Env (optional):
#   PG_DSN     defaults to localhost dev DSN baked into Makefile
#   BACKUP_DIR overrides the positional output_dir argument
#
# The custom format (`pg_dump -F c`) is portable across PG minor
# versions, restores in parallel, and preserves the schema_migrations
# row so the restored DB declares its own migration version. Plain SQL
# dumps were rejected: they re-run migrations on restore, which can
# diverge from the dump's actual state.
#
# This script is intentionally simple: no encryption, no off-site
# upload, no rotation. Compose those around it. See
# docs/runbook/backup-recovery.md.
set -euo pipefail

DSN="${PG_DSN:-postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable}"
DEFAULT_DIR="${BACKUP_DIR:-${1:-./backups}}"
mkdir -p "$DEFAULT_DIR"

ts="$(date -u +%Y%m%d-%H%M%SZ)"
out="$DEFAULT_DIR/agentlens-$ts.dump"

if ! command -v pg_dump >/dev/null 2>&1; then
  echo "pg_dump not found in PATH. Install postgresql-client or run via 'docker compose exec postgres pg_dump'." >&2
  exit 1
fi

echo "→ dumping to $out"
pg_dump --dbname="$DSN" --format=custom --compress=9 --file="$out"

# Record integrity hash so the restore step can spot bit-rot later.
hash_cmd="$(command -v sha256sum || command -v shasum)"
if [[ -z "$hash_cmd" ]]; then
  echo "warning: no sha256sum / shasum found; skipping integrity hash" >&2
else
  if [[ "$hash_cmd" == *shasum ]]; then
    "$hash_cmd" -a 256 "$out" > "$out.sha256"
  else
    "$hash_cmd" "$out" > "$out.sha256"
  fi
fi

bytes="$(wc -c <"$out" | tr -d ' ')"
echo "✓ wrote $out ($bytes bytes)"
[[ -f "$out.sha256" ]] && echo "✓ wrote $out.sha256"
