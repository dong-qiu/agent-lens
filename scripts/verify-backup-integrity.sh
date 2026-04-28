#!/usr/bin/env bash
# verify-backup-integrity.sh — full round-trip integrity test:
#   1. dump current PG_DSN with pg-backup.sh
#   2. restore into a fresh transient database under the SAME server
#   3. start a transient agent-lens collector pointed at it
#   4. run agent-lens-hook verify against a known session id
#   5. tear everything down
#
# This is the operational answer to "is my backup actually usable?"
# It exercises every layer the recovery procedure depends on:
# pg_dump output, pg_restore semantics, the events hash chain, and
# the collector's read path. A green run means a real restore would
# also pass agent-lens-hook verify.
#
# Usage:
#   scripts/verify-backup-integrity.sh <session_id> [--keep-dump <path>]
#
# Env (optional):
#   PG_DSN              source DSN (defaults to dev DSN)
#   AGENT_LENS_BIN      path to compiled `agent-lens` (defaults to
#                       `go run ./cmd/agent-lens`, which works from repo root)
#   AGENT_LENS_HOOK_BIN path to compiled `agent-lens-hook` (defaults to
#                       /Users/dongqiu/go/bin/agent-lens-hook then PATH)
#   VERIFY_PORT         transient collector port (default 18787)
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <session_id> [--keep-dump <path>]" >&2
  exit 2
fi
session_id="$1"; shift

keep_dump=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-dump) keep_dump="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

DSN="${PG_DSN:-postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable}"
PORT="${VERIFY_PORT:-18787}"

# swap_db <dsn> <new_dbname>
# Replaces the /<dbname> path component of a libpq URL with a
# different database name, preserving any ?query string.
# Implemented via plain string slicing rather than sed/glob to dodge
# every BSD/GNU sed and bash-glob escaping pitfall in this code path.
swap_db() {
  local dsn="$1" new="$2"
  local before query
  if [[ "$dsn" == *"?"* ]]; then
    before="${dsn%%\?*}"     # everything before the first '?'
    query="?${dsn#*\?}"      # '?' onwards
  else
    before="$dsn"
    query=""
  fi
  local prefix="${before%/*}"  # strip the last /<dbname>
  echo "${prefix}/${new}${query}"
}

# Resolve hook binary: explicit env > known dev path > $PATH.
if [[ -n "${AGENT_LENS_HOOK_BIN:-}" ]]; then
  hook="$AGENT_LENS_HOOK_BIN"
elif [[ -x "$HOME/go/bin/agent-lens-hook" ]]; then
  hook="$HOME/go/bin/agent-lens-hook"
elif command -v agent-lens-hook >/dev/null 2>&1; then
  hook="$(command -v agent-lens-hook)"
else
  echo "agent-lens-hook not found. Build it (make build) or set AGENT_LENS_HOOK_BIN." >&2
  exit 1
fi

tmpdir="$(mktemp -d -t agentlens-verify.XXXXXX)"
dump="$tmpdir/source.dump"
collector_log="$tmpdir/collector.log"
collector_pid=""
restore_db="agentlens_verify_$$"
restore_dsn="$(swap_db "$DSN" "$restore_db")"

cleanup() {
  set +e
  if [[ -n "$collector_pid" ]]; then
    kill "$collector_pid" 2>/dev/null || true
    wait "$collector_pid" 2>/dev/null || true
  fi
  # Drop the transient DB. Connect via the postgres template DB to
  # avoid "DB in use by current backend".
  admin_dsn="$(swap_db "$DSN" postgres)"
  psql "$admin_dsn" -c "DROP DATABASE IF EXISTS $restore_db" >/dev/null 2>&1
  if [[ -n "$keep_dump" && -f "$dump" ]]; then
    mv "$dump" "$keep_dump"
    [[ -f "$dump.sha256" ]] && mv "$dump.sha256" "$keep_dump.sha256"
    echo "→ kept dump at $keep_dump"
    rm -rf "$tmpdir"
  else
    rm -rf "$tmpdir"
  fi
}
trap cleanup EXIT

echo "[1/4] dumping source database..."
PG_DSN="$DSN" BACKUP_DIR="$tmpdir" \
  scripts/pg-backup.sh "$tmpdir" >/dev/null
# pg-backup writes agentlens-<ts>.dump; our cleanup expects $dump
mv "$tmpdir"/agentlens-*.dump "$dump"
[[ -f "$tmpdir"/agentlens-*.dump.sha256 ]] && mv "$tmpdir"/agentlens-*.dump.sha256 "$dump.sha256"

echo "[2/4] restoring into transient database $restore_db..."
admin_dsn="$(swap_db "$DSN" postgres)"
psql "$admin_dsn" -c "CREATE DATABASE $restore_db" >/dev/null
scripts/pg-restore.sh "$dump" "$restore_dsn" >/dev/null

# Refuse to start if something else is already on $PORT — most often
# that's an orphan transient collector from an earlier failed run, in
# which case healthz would 200 against the wrong DB and we'd report a
# false success.
if lsof -iTCP:"$PORT" -sTCP:LISTEN -P >/dev/null 2>&1; then
  echo "port $PORT already bound. Run \`lsof -ti:$PORT | xargs kill\` to clear it, or set VERIFY_PORT=<other>." >&2
  exit 1
fi

echo "[3/4] building + starting transient collector on :$PORT..."
# Pre-build instead of `go run` so the collector's PID is the real
# binary's PID — kill at cleanup actually terminates it. With `go run`
# the spawned child becomes orphaned and keeps the port held.
collector_bin="$tmpdir/agent-lens"
go build -o "$collector_bin" ./cmd/agent-lens
AGENT_LENS_PG_DSN="$restore_dsn" \
AGENT_LENS_ADDR=":$PORT" \
"$collector_bin" >"$collector_log" 2>&1 &
collector_pid=$!

# Wait for /healthz.
for _ in $(seq 1 30); do
  if curl -s --noproxy '*' -m 1 "http://localhost:$PORT/healthz" -o /dev/null -w "%{http_code}" 2>/dev/null \
      | grep -q 200; then
    break
  fi
  sleep 0.5
done

if ! curl -s --noproxy '*' -m 1 "http://localhost:$PORT/healthz" -o /dev/null -w "%{http_code}" 2>/dev/null \
    | grep -q 200; then
  echo "collector failed to start. Tail of $collector_log:"
  tail -20 "$collector_log"
  exit 1
fi

echo "[4/4] verifying hash chain for session $session_id..."
verify_out="$tmpdir/verify.out"
"$hook" verify --session "$session_id" --url "http://localhost:$PORT" --quiet \
  > >(tee "$verify_out") 2>&1
status=$?

# verify exits 0 on "no events" too, which would let an empty restore
# masquerade as a passing backup. Treat zero-event output as failure.
if grep -q "no events" "$verify_out" 2>/dev/null; then
  echo "✗ verify FAILED — restored DB has no events for session $session_id" >&2
  exit 1
fi

if [[ $status -eq 0 ]]; then
  echo "✓ backup is valid: hash chain intact for session $session_id after dump+restore round-trip"
else
  echo "✗ verify FAILED — backup did not preserve hash chain integrity"
fi

exit $status
