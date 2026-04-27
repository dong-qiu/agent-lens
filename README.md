# Agent Lens

面向 Coding Agent 的透明可审计系统。捕获开发者 ↔ Coding Agent 的交互、Agent 内部推理（thinking）、工具调用与下游产出（commit / PR / build / deploy），串成一条可验证的证据链。

设计与决策见 [`SPEC.md`](./SPEC.md)（v0.4）。Claude Code 工作指引见 [`CLAUDE.md`](./CLAUDE.md)。

## 状态

M1 开发中。后端 + hook + GraphQL 已就绪并有测试覆盖，UI 时间线视图已落第一版。线上 dogfood 计划在 M3 完成后激活（SPEC §17）。

## 一次跑通

```bash
# 1. 装工具链（macOS）
brew install go pnpm buf node golang-migrate

# 2. 启 Postgres + MinIO
make compose-up
make migrate-up

# 3. 起后端（terminal 1）
make build && ./bin/agent-lens
# 监听 :8787，提供 /healthz, /v1/events (POST), /v1/graphql, /v1/playground

# 4. 起前端（terminal 2）
make web-install   # 首次
make web-dev       # http://localhost:5173

# 5. 装 Claude Code hook
# 编辑 ~/.claude/settings.json，加入 hooks 指向 ./bin/agent-lens-hook claude
# 详见下方 "接入 Claude Code"

# 6. 装 git post-commit hook（在被观测的仓库里）
ln -s "$(pwd)/bin/agent-lens-hook" .git/hooks/agent-lens-hook
cat > .git/hooks/post-commit <<'EOF'
#!/bin/sh
exec .git/hooks/agent-lens-hook git-post-commit
EOF
chmod +x .git/hooks/post-commit
```

## 测试

```bash
make test              # Go 单元 + handler 集成（无需 Docker）
make test-integration  # Postgres testcontainers（需 Docker）
make web-build         # TS 类型检查 + Vite 打包
```

## 接入 Claude Code

在 `~/.claude/settings.json`（或仓库级 `.claude/settings.json`）添加：

```json
{
  "hooks": {
    "UserPromptSubmit":  [{"hooks": [{"type": "command", "command": "/absolute/path/to/agent-lens-hook claude"}]}],
    "PreToolUse":        [{"hooks": [{"type": "command", "command": "/absolute/path/to/agent-lens-hook claude"}]}],
    "PostToolUse":       [{"hooks": [{"type": "command", "command": "/absolute/path/to/agent-lens-hook claude"}]}],
    "Stop":              [{"hooks": [{"type": "command", "command": "/absolute/path/to/agent-lens-hook claude"}]}],
    "SessionStart":      [{"hooks": [{"type": "command", "command": "/absolute/path/to/agent-lens-hook claude"}]}]
  }
}
```

环境变量：
- `AGENT_LENS_URL`（默认 `http://localhost:8787`）
- `AGENT_LENS_TOKEN`（可选 bearer token）
- `AGENT_LENS_CURSOR_DIR`（默认 `~/.agent-lens/cursors`，存 transcript 增量读 cursor）

Stop hook 会读 `transcript_path` 提取 `thinking` / `text` content blocks（仅当本轮启用 extended thinking 时有 thinking）。HTTP 失败时回落 `~/.agent-lens/sessions/<sid>.ndjson` 文件 sink。

## 模块名

`go.mod` 的 `github.com/dongqiu/agent-lens` 是占位。落定 GitHub 组织后用 `go mod edit -module <new>` 替换。

## 许可

待定。
