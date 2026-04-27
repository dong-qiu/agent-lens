#!/usr/bin/env bash
# send.sh — invoked by actions/build/action.yml.
#
# Constructs a single Agent Lens build event from the surrounding
# GitHub Actions environment, optionally hashes artifact files matching
# the user-supplied globs, and POSTs the event as NDJSON to /v1/events.
#
# Required env (passed by action.yml inputs):
#   AGENT_LENS_URL          - Server base URL (no trailing slash)
# Optional env:
#   AGENT_LENS_TOKEN        - Bearer token; empty = no Authorization header
#   AGENT_LENS_STATUS       - Build status string (e.g. job.status)
#   AGENT_LENS_ARTIFACTS    - Newline-separated globs to hash
#   AGENT_LENS_SESSION_ID   - Override default session id

set -euo pipefail

: "${AGENT_LENS_URL:?AGENT_LENS_URL is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is missing — only meaningful inside GitHub Actions}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is missing — only meaningful inside GitHub Actions}"

url="${AGENT_LENS_URL%/}"
session_id="${AGENT_LENS_SESSION_ID:-github-build:${GITHUB_REPOSITORY}/${GITHUB_RUN_ID}}"
artifacts_input="${AGENT_LENS_ARTIFACTS:-}"
actor_id="${GITHUB_WORKFLOW:-github-actions}"

# Pick the right sha256 / stat tool for whichever runner OS we're on.
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- "$1" | awk '{print $1}'
  else
    shasum -a 256 -- "$1" | awk '{print $1}'
  fi
}
size_file() {
  if stat --version >/dev/null 2>&1; then
    stat -c%s -- "$1"
  else
    stat -f%z -- "$1"
  fi
}

# Walk every glob the user provided. Globs that match no files are
# silently skipped — matches the intent "hash whatever is there".
shopt -s nullglob
artifact_paths=()
artifact_shas=()
artifact_bytes=()
if [[ -n "$artifacts_input" ]]; then
  while IFS= read -r glob; do
    [[ -z "$glob" ]] && continue
    # shellcheck disable=SC2086
    for f in $glob; do
      [[ -f "$f" ]] || continue
      artifact_paths+=("$f")
      artifact_shas+=("$(hash_file "$f")")
      artifact_bytes+=("$(size_file "$f")")
    done
  done <<<"$artifacts_input"
fi
shopt -u nullglob

# Hand the hashes to Python via env so we don't have to JSON-escape in
# bash. Python is preinstalled on every GitHub runner image.
export AL_ARTIFACTS_PATHS="$(printf '%s\n' "${artifact_paths[@]:-}")"
export AL_ARTIFACTS_SHAS="$(printf '%s\n' "${artifact_shas[@]:-}")"
export AL_ARTIFACTS_BYTES="$(printf '%s\n' "${artifact_bytes[@]:-}")"
export AL_SESSION_ID="$session_id"
export AL_ACTOR_ID="$actor_id"
export AL_STATUS="${AGENT_LENS_STATUS:-}"

event=$(python3 - <<'PY'
import json, os

def split(name):
    raw = os.environ.get(name, "").strip("\n")
    return [s for s in raw.split("\n") if s]

paths = split("AL_ARTIFACTS_PATHS")
shas = split("AL_ARTIFACTS_SHAS")
sizes = split("AL_ARTIFACTS_BYTES")
artifacts = [
    {"path": p, "sha256": s, "bytes": int(b)}
    for p, s, b in zip(paths, shas, sizes)
]

head_sha = os.environ.get("GITHUB_SHA", "")
refs = ["git:" + head_sha] if head_sha else []

event = {
    "session_id": os.environ["AL_SESSION_ID"],
    "actor": {"type": "system", "id": os.environ["AL_ACTOR_ID"]},
    "kind": "build",
    "payload": {
        "source": "composite-action",
        "status": os.environ.get("AL_STATUS", ""),
        "workflow": os.environ.get("GITHUB_WORKFLOW", ""),
        "job": os.environ.get("GITHUB_JOB", ""),
        "run_id": os.environ.get("GITHUB_RUN_ID", ""),
        "run_number": os.environ.get("GITHUB_RUN_NUMBER", ""),
        "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", ""),
        "ref": os.environ.get("GITHUB_REF", ""),
        "sha": head_sha,
        "actor": os.environ.get("GITHUB_ACTOR", ""),
        "artifacts": artifacts,
    },
    "refs": refs,
}
print(json.dumps(event))
PY
)

curl_args=(-fsS -X POST "${url}/v1/events" -H 'Content-Type: application/x-ndjson')
if [[ -n "${AGENT_LENS_TOKEN:-}" ]]; then
  curl_args+=(-H "Authorization: Bearer ${AGENT_LENS_TOKEN}")
fi

printf '%s\n' "$event" | curl "${curl_args[@]}" --data-binary @-

echo "agent-lens: build event sent (session=${session_id}, status=${AL_STATUS:-(unset)}, artifacts=${#artifact_paths[@]})"
