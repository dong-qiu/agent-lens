# Agent Lens Build Reporter

Composite GitHub Action. Drop into a workflow to ship a `build` event to your Agent Lens server with the run's status and SHA-256 hashes of any artifacts you point at.

## Why

The `workflow_run` webhook (M2-C-1) gives you outside-in lifecycle visibility (queued → in_progress → completed) but no artifact info. This action runs **inside** the job and lands one event with:

- the run's final `status`
- a list of `{path, sha256, bytes}` per matched artifact glob
- the same `session_id` (`github-build:<owner>/<repo>/<run_id>`) the webhook uses, so both views land in one timeline session

## Usage

```yaml
- name: Build
  run: make build

- name: Report build to Agent Lens
  if: always()  # report even when build failed
  uses: dong-qiu/agent-lens/actions/build@main
  with:
    url: https://lens.example.com
    token: ${{ secrets.AGENT_LENS_TOKEN }}
    status: ${{ job.status }}
    artifacts: |
      dist/*.tar.gz
      build/*.bin
```

See [`examples/github-actions/build.yml`](../../examples/github-actions/build.yml) for a full workflow.

## Inputs

| Name | Required | Default | Description |
|---|---|---|---|
| `url` | yes | — | Agent Lens server URL, no trailing slash |
| `token` | no | empty | Bearer token. Empty = unauthenticated (only OK if the server has `AGENT_LENS_TOKEN` unset) |
| `status` | no | empty | Build status string, typically `${{ job.status }}` |
| `artifacts` | no | empty | Newline-separated globs to hash. Globs that match nothing are skipped silently |
| `session-id` | no | `github-build:$REPO/$RUN_ID` | Override session id; the default matches the webhook so they share a session |

## Output payload shape

```json
{
  "kind": "build",
  "session_id": "github-build:acme/widget/123456789",
  "actor": {"type": "system", "id": "CI"},
  "payload": {
    "source": "composite-action",
    "status": "success",
    "workflow": "CI",
    "job": "test",
    "run_id": "123456789",
    "run_attempt": "1",
    "sha": "deadbeefcafe1234...",
    "actor": "alice",
    "artifacts": [
      {"path": "dist/x.tar.gz", "sha256": "<hex>", "bytes": 1234}
    ]
  },
  "refs": ["git:deadbeefcafe1234..."]
}
```

`refs[git:<head_sha>]` is what the linking worker uses to connect this build to the corresponding `commit`, `pr`, `review`, and `push` events.

## Re-runs

Re-running a workflow attempt produces a new event (the server fills a new ULID per call) — they're distinct attempts, not duplicates. The webhook's `run_attempt` lifecycle deliveries are still deduplicated via `X-GitHub-Delivery`.

## Runtime requirements

- **bash 4+** (default on every GitHub-hosted runner)
- **python3** (preinstalled on every GitHub-hosted runner; used to build the JSON payload without bash quoting hell)
- **sha256sum** (Linux) or **shasum** (macOS) — auto-detected
- **GNU stat** (Linux) or **BSD stat** (macOS) — auto-detected

Self-hosted runners need to provide bash + python3.

## Runner OS support

Exercised on `ubuntu-latest` (Linux) by [`smoke_test.sh`](./smoke_test.sh) which the `action-smoke` CI job runs on every PR. macOS runners pass the same code paths via the `shasum` / BSD-stat branches but aren't gated in CI today. Windows runners are not supported.

## Local smoke test

```bash
bash actions/build/smoke_test.sh
```

Mocks `curl` and validates that send.sh produces well-formed JSON for a fixture of three artifact files.
