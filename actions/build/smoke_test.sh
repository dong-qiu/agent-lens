#!/usr/bin/env bash
# smoke_test.sh — exercises actions/build/send.sh end-to-end without
# actually POSTing.
#
# Strategy: shadow `curl` with a script that drains stdin into a file,
# run send.sh against a tmpdir of fake artifacts, then validate the
# captured NDJSON has the fields we expect. Run by the
# `action-smoke` job in .github/workflows/ci.yml on every PR.

set -euo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
send_sh="$script_dir/send.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Fake artifacts: top-level matches, nested match, glob with no match.
echo "binary content" > "$tmp/artifact-a.tar.gz"
echo "more"           > "$tmp/artifact-b.tar.gz"
mkdir -p "$tmp/sub"
echo "nested"         > "$tmp/sub/inner.bin"

# Mock curl that drains stdin to $CAPTURED. The mock takes the same
# args send.sh passes; we just ignore them.
cat > "$tmp/curl" <<'EOF'
#!/usr/bin/env bash
cat > "$CAPTURED"
EOF
chmod +x "$tmp/curl"

(
  cd "$tmp"
  PATH="$tmp:$PATH" \
    AGENT_LENS_URL=https://example.invalid \
    AGENT_LENS_TOKEN=secret \
    AGENT_LENS_STATUS=success \
    AGENT_LENS_ARTIFACTS=$'artifact-*.tar.gz\nsub/*.bin\nno-such-glob/*' \
    GITHUB_REPOSITORY=acme/widget \
    GITHUB_RUN_ID=123 \
    GITHUB_RUN_NUMBER=1 \
    GITHUB_RUN_ATTEMPT=1 \
    GITHUB_SHA=deadbeefcafe1234567890abcdef0123456789ab \
    GITHUB_REF=refs/heads/main \
    GITHUB_WORKFLOW=CI \
    GITHUB_JOB=test \
    GITHUB_ACTOR=alice \
    CAPTURED="$tmp/captured.ndjson" \
    bash "$send_sh" >/dev/null
)

python3 - "$tmp/captured.ndjson" <<'PY'
import json, sys

path = sys.argv[1]
with open(path) as f:
    line = f.read().rstrip("\n")
data = json.loads(line)

def expect(name, got, want):
    if got != want:
        print(f"FAIL: {name} = {got!r}, want {want!r}", file=sys.stderr)
        sys.exit(1)

expect("session_id", data["session_id"], "github-build:acme/widget/123")
expect("kind", data["kind"], "build")
expect("actor", data["actor"], {"type": "system", "id": "CI"})
expect("payload.status", data["payload"]["status"], "success")
expect("payload.source", data["payload"]["source"], "composite-action")
expect("payload.workflow", data["payload"]["workflow"], "CI")
expect("payload.run_id", data["payload"]["run_id"], "123")
expect("refs", data["refs"], ["git:deadbeefcafe1234567890abcdef0123456789ab"])

artifacts = data["payload"]["artifacts"]
if len(artifacts) != 3:
    print(f"FAIL: expected 3 artifacts, got {len(artifacts)}: {artifacts}", file=sys.stderr)
    sys.exit(1)

paths = {a["path"] for a in artifacts}
expected = {"artifact-a.tar.gz", "artifact-b.tar.gz", "sub/inner.bin"}
if paths != expected:
    print(f"FAIL: artifact paths {paths} != {expected}", file=sys.stderr)
    sys.exit(1)

for a in artifacts:
    if not a["sha256"] or len(a["sha256"]) != 64:
        print(f"FAIL: bad sha256 on {a['path']}: {a['sha256']!r}", file=sys.stderr)
        sys.exit(1)
    if a["bytes"] <= 0:
        print(f"FAIL: bad bytes on {a['path']}: {a['bytes']}", file=sys.stderr)
        sys.exit(1)

print("smoke test OK: 3 artifacts, session/kind/refs/payload all match")
PY
