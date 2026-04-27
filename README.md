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
# 监听 :8787，提供 /healthz, /v1/events (POST), /v1/graphql
# /v1/playground 仅在 AGENT_LENS_PLAYGROUND=true 时挂载

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

### 环境变量

**Server (`agent-lens`)**
- `AGENT_LENS_ADDR`（默认 `:8787`）
- `AGENT_LENS_PG_DSN`（默认本地 compose 配置）
- `AGENT_LENS_TOKEN`：bearer token；空则 `/v1` 不鉴权（dev 默认）。配置后 hook 与浏览器都需带 `Authorization: Bearer <token>`
- `AGENT_LENS_PLAYGROUND`：设为 `true` 才挂载 `/v1/playground`（默认 off，避免生产暴露 introspection）
- `AGENT_LENS_GH_WEBHOOK_SECRET`：GitHub webhook 共享密钥；空则 `/webhooks/github` 不挂载。设置后 server 用 HMAC-SHA256 校验 `X-Hub-Signature-256`

**Hook (`agent-lens-hook`)**
- `AGENT_LENS_URL`（默认 `http://localhost:8787`）
- `AGENT_LENS_TOKEN`（同 server，作为 bearer token 发送）
- `AGENT_LENS_CURSOR_DIR`（默认 `~/.agent-lens/cursors`，存 transcript 增量读 cursor）

Stop hook 会读 `transcript_path` 提取 `thinking` / `text` content blocks（仅当本轮启用 extended thinking 时有 thinking）。HTTP 失败时回落 `~/.agent-lens/sessions/<sid>.ndjson` 文件 sink。

## 接入 GitHub（M2-A：PR 事件）

1. 在 server 端设环境变量 `AGENT_LENS_GH_WEBHOOK_SECRET=<random>`，然后启动 / 重启 `agent-lens`
2. 在被观测仓库的 GitHub 设置 → Webhooks → Add webhook：
   - Payload URL：`https://<your-server>/webhooks/github`
   - Content type：`application/json`
   - Secret：与 `AGENT_LENS_GH_WEBHOOK_SECRET` 相同
   - 选 "Let me select individual events" → 勾 `Pull requests` / `Pull request reviews` / `Pushes` / `Workflow runs`
3. 事件映射到 session：
   - `pull_request` → `kind=pr`，session `github-pr:<owner>/<repo>/<number>`
   - `pull_request_review` → `kind=review`，**复用 PR 的 session**，所以 review 跟 PR 在同一时间线
   - `push` → `kind=push`，session `github-push:<owner>/<repo>/<branch>`
   - `workflow_run` → `kind=build`，session `github-build:<owner>/<repo>/<run_id>`，三种 lifecycle（requested / in_progress / completed）汇入同一 run session
4. 全部事件都把相关 commit SHA 写入 `refs[git:<sha>]`，linking worker 自动跟本地 commit hook 上报的 COMMIT 事件串联

## 在 CI 里上报 build 事件 + artifact hash

webhook 路径（M2-C-1）只能拿到 GitHub 那边的 lifecycle，**没有 artifact 信息**。要让 build 事件携带 artifact 的 SHA-256，在 workflow 里 `uses:` composite Action：

```yaml
- run: make build
- if: always()
  uses: dong-qiu/agent-lens/actions/build@main
  with:
    url: ${{ vars.AGENT_LENS_URL }}
    token: ${{ secrets.AGENT_LENS_TOKEN }}
    status: ${{ job.status }}
    artifacts: |
      dist/*.tar.gz
      bin/*
```

详细文档见 [`actions/build/README.md`](./actions/build/README.md)，完整示例 [`examples/github-actions/build.yml`](./examples/github-actions/build.yml)。session_id 跟 webhook 对齐（`github-build:<owner>/<repo>/<run_id>`），两条腿事件汇入同一时间线。

## 模块名

`go.mod` 的 `github.com/dongqiu/agent-lens` 是占位。落定 GitHub 组织后用 `go mod edit -module <new>` 替换。

## 许可

待定。
