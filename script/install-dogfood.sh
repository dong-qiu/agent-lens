#!/usr/bin/env bash
# install-dogfood.sh — opt in to §17 self-observation for THIS clone of
# agent-lens. Idempotent; safe to re-run.
#
# What it does:
#   1. Builds agent-lens-hook from source and stages it in $GOBIN (or
#      reports the pre-built binary if it's already on PATH).
#   2. Copies .claude/settings.example.json to .claude/settings.local.json
#      (gitignored) so Claude Code starts firing hooks for this dir only.
#   3. Installs .git/hooks/post-commit pointing at agent-lens-hook
#      git-post-commit. (Per-clone; never committed.)
#   4. Prints next steps for spinning up a local agent-lens server.
#
# What it does NOT do:
#   - Touch your home directory (no ~/.agent-lens/ writes from this script).
#   - Modify .claude/settings.json itself (project-shared config stays clean).
#   - Auto-start agent-lens. You run that yourself; that way the hook only
#     surfaces in Claude Code when you've explicitly opted in.

set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

say() { printf "\033[1;34m==>\033[0m %s\n" "$*"; }
ok()  { printf "  \033[32m✓\033[0m %s\n" "$*"; }
warn() { printf "  \033[33m!\033[0m %s\n" "$*"; }

say "1/3 build agent-lens-hook"
if ! command -v go >/dev/null 2>&1; then
  echo "go toolchain required; install from https://go.dev/dl" >&2
  exit 1
fi
go install ./cmd/agent-lens-hook
HOOK_BIN="$(go env GOBIN)"
[ -z "$HOOK_BIN" ] && HOOK_BIN="$(go env GOPATH)/bin"
ok "agent-lens-hook installed at $HOOK_BIN/agent-lens-hook"
case ":$PATH:" in
  *":$HOOK_BIN:"*) ok "$HOOK_BIN already on PATH" ;;
  *) warn "$HOOK_BIN is NOT on your PATH. Add: export PATH=\"$HOOK_BIN:\$PATH\"" ;;
esac

say "2/3 register Claude Code hooks (.claude/settings.local.json)"
if [ -f .claude/settings.local.json ]; then
  warn ".claude/settings.local.json already exists — leaving it alone"
else
  cp .claude/settings.example.json .claude/settings.local.json
  ok "wrote .claude/settings.local.json (gitignored; edit freely)"
fi

say "3/3 install .git/hooks/post-commit"
if [ -f .git/hooks/post-commit ] && ! grep -q "agent-lens-hook" .git/hooks/post-commit 2>/dev/null; then
  warn ".git/hooks/post-commit exists and isn't from us — leaving it alone"
else
  cat > .git/hooks/post-commit <<'EOF'
#!/usr/bin/env bash
# Installed by script/install-dogfood.sh. Sends a kind=commit event to
# the local agent-lens server. Runs in the foreground but exits 0 on
# any failure so it never blocks the commit. Remove this file to opt
# out.
exec agent-lens-hook git-post-commit
EOF
  chmod +x .git/hooks/post-commit
  ok "wrote .git/hooks/post-commit"
fi

cat <<EOF

\033[1;32mDone.\033[0m Next:
  1) Start a local agent-lens server:
       AGENT_LENS_STORE=memory go run ./cmd/agent-lens
     (Or use Postgres — see deploy/compose/.)
  2) In a new shell, open Claude Code in this directory. Hooks fire to
     http://localhost:8787 by default; events appear under session_id
     'claude-code:<uuid>'.
  3) Verify the chain after a few prompts:
       agent-lens-hook verify --session 'claude-code:<your-session-id>'

To opt out: rm .claude/settings.local.json .git/hooks/post-commit
EOF
