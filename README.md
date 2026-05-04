# Agent Lens

面向 Coding Agent 的透明可审计系统。捕获开发者 ↔ Coding Agent 的交互、Agent 内部推理（thinking）、工具调用与下游产出（commit / PR / build / deploy），串成一条可验证的证据链。

设计文档分两层：[`SPEC.md`](./SPEC.md) 描述系统**现在是什么**，[`docs/ADR/`](./docs/ADR/README.md) 是**为什么这么决定**的决策档案（含 ADR 协作约定）。Claude Code 工作指引见 [`CLAUDE.md`](./CLAUDE.md)。

## 状态

M1–M3 已交付，§17 dogfood 已激活——本仓库自身就是首个使用者。v0.1.0 personal-mode 发布筹备中（详见 [`docs/ADR/0006-release-and-distribution-v0.1.md`](./docs/ADR/0006-release-and-distribution-v0.1.md)）；当前安装路径仍是 build-from-source（见 §"一次跑通"）。Team mode、Helm chart、Windows、PR Review Bot outbound 等不在 v0.1 范围。

## 一次跑通

```bash
# 1. 装工具链（macOS）
brew install go pnpm buf node
# golang-migrate 不再需要——server 启动时自动应用嵌入的 migrations
# （如需外部迁移：AGENT_LENS_SKIP_MIGRATE=1 + brew install golang-migrate）

# 2. 启 Postgres + MinIO
make compose-up

# 3a. 开发模式：起后端（terminal 1）+ Vite 前端（terminal 2）
make build && ./bin/agent-lens
# 启动时自动 migrate up；监听 :8787 提供 /healthz, /v1/events (POST), /v1/graphql
# /v1/playground 仅在 AGENT_LENS_PLAYGROUND=true 时挂载
make web-install   # 首次
make web-dev       # http://localhost:5173 (Vite，proxies /v1 到 :8787)

# 3b. 生产模式：UI 嵌入二进制由 / 直接服务
make build-prod && ./bin/agent-lens
# 浏览 http://localhost:8787 看 Lens UI；同端口走 GraphQL 与 API

# 4. 装 Claude Code hook
# 编辑 ~/.claude/settings.json，加入 hooks 指向 ./bin/agent-lens-hook claude
# 详见下方 "接入 Claude Code"

# 5. 装 git post-commit hook（在被观测的仓库里）
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
- `AGENT_LENS_GH_WEBHOOK_SECRET`：GitHub webhook 共享密钥；空则 `/webhooks/github` 返 503。设置后 server 用 HMAC-SHA256 校验 `X-Hub-Signature-256`
- `AGENT_LENS_DEPLOY_WEBHOOK_TOKEN`：deploy webhook 独立 bearer token（与 `AGENT_LENS_TOKEN` 分离）；空则 `/webhooks/deploy` 返 503

**Hook (`agent-lens-hook`)**
- `AGENT_LENS_URL`（默认 `http://localhost:8787`）
- `AGENT_LENS_TOKEN`（同 server，作为 bearer token 发送）
- `AGENT_LENS_CURSOR_DIR`（默认 `~/.agent-lens/cursors`，存 transcript 增量读 cursor）

Stop hook 会读 `transcript_path` 提取 `thinking` / `text` content blocks（仅当本轮启用 extended thinking 时有 thinking）。HTTP 失败时回落 `~/.agent-lens/sessions/<sid>.ndjson` 文件 sink。

## Attestation 签名密钥（M3-B-1）

为后续导出 in-toto / SLSA attestation 准备本地 ed25519 密钥：

```bash
agent-lens-hook keygen
# 默认写到 $HOME/.agent-lens/keys/ed25519 (私钥, 0600) + .pub (公钥, 0644)
# PEM-encoded (PKCS#8 / PKIX)，cosign 和 openssl 都能读
```

`--out <path>` 可改路径。**拒绝覆盖已有文件**——轮换密钥写到新路径，避免在生产管道里悄悄抹掉私钥。

`internal/attest` 包提供 `Sign` / `Verify` 走 DSSE envelope（`https://github.com/secure-systems-lab/dsse`），ed25519 走 stdlib。Sigstore（Fulcio + Rekor）网络签名作为后续可选项，与现有 API 同接口、按 flag 切换；M3-B-1 只做离线密钥这条腿。

预算用法：
- `agent-lens-hook export code-provenance` → in-toto Statement，predicateType `agent-lens.dev/code-provenance/v1`
- `agent-lens-hook export slsa-build` → 标准 SLSA Build Track v1
- `agent-lens-hook export deploy-evidence` → predicateType `agent-lens.dev/deploy-evidence/v1`

每条命令产出一个 DSSE 信封 `.intoto.jsonl`，cosign 兼容。Predicate 里只放 thinking / prompt 的 sha256 + 200 字预览 + token 数；全文留 agent-lens 存储里——签了就难撤回，敏感内容上链得万分谨慎。

### 导出 code-provenance（commit 边界）

```bash
agent-lens-hook export code-provenance \
  --commit <git-sha> \
  --session <claude-session-id> \
  --repo https://github.com/<owner>/<repo> \
  --out attestation.intoto.jsonl
```

subject = git commit；predicate 列出贡献到此 commit 的 prompt / thought / tool_call 事件（每条带 sha256 + 200 字预览）。

### 导出 SLSA build provenance（build 边界）

标准 `https://slsa.dev/provenance/v1`，cosign / slsa-verifier 直接吃：

```bash
agent-lens-hook export slsa-build \
  --session github-build:<owner>/<repo>/<run_id> \
  --repo https://github.com/<owner>/<repo> \
  --out slsa.intoto.jsonl
```

需要 session 里有 [composite Action](./actions/build/) 上报的 `kind=BUILD source=composite-action` 事件——它的 `payload.artifacts` 提供了 SLSA 强制的 subjects。`workflow_run` webhook 单独不够（没有 artifact hash）。

self-hosted runner 用 `--builder-id` 覆盖默认的 GitHub-hosted URI。

### 导出 deploy-evidence（deploy 边界）

deploy 事件本身已经在 `/webhooks/deploy` 落地（M3-A）；M3-B-4 这条命令把它升级为可签名的 in-toto attestation：

```bash
agent-lens-hook export deploy-evidence \
  --event <deploy-event-id> \
  --build-attestation slsa.intoto.jsonl \
  --code-attestation code-prov.intoto.jsonl \
  --out deploy.intoto.jsonl
```

- subject = 容器镜像（取 `image` 字段当 name、`image_digest` 当 sha256）。
- predicate.environment / cluster / namespace / status 等都直接来自 deploy webhook payload。
- `--build-attestation` / `--code-attestation` 都是可选；命令会对文件做 sha256 然后写到 `predicate.upstream.{build,code}_attestation`，供 verifier 顺着 deploy → build → code 走完证据图。不传就是空字符串，相当于明示"上游证据缺失"。
- `predicate.trace_root_event_id` 默认就是 deploy event 自身的 id；查 store 时直接当入口。

查事件 id 的两种方式：
- POST `/webhooks/deploy` 时带 `Idempotency-Key: <ulid>`——这个 key 同时被服务器当成 event id 用，客户端预生成、自己留底。
- 没设 `Idempotency-Key` 时只能事后用 GraphQL `events(sessionId: "deploy:<env>", limit: 10)` 查时间线（响应里的 `id` 字段）。

### 导出 audit-report（整条证据链打包）

把一个 root event id 起点的整条证据链打成单文件 JSON，包含所有相关 session 的 events、哈希链、可选嵌入的 attestations，以及 manifest sha256，自鉴防篡改。

```bash
agent-lens-hook export audit-report \
  --root <event-id> \
  --attestation deploy.intoto.jsonl \
  --attestation slsa.intoto.jsonl \
  --attestation code.intoto.jsonl \
  --out audit-report.json
```

- `--root` 取一个 deploy / commit / pr / build event id；BFS 沿 `event.links` 把所有可达 session 拉进来（默认上限 50 session，靠 `--max-sessions` 调）。事件 id 的来源跟 deploy webhook 一节一致：客户端预生成的 `Idempotency-Key`（双用作 event id），或事后 `events(sessionId, limit)` 查 GraphQL 取第一个/最近一个事件。
- `--attestation` 可重复，把 `.intoto.jsonl` 文件原样嵌入；记录 sha256，verifier 可以脱机比对。
- 输出 JSON 顶层有 `manifest.{sessions_sha256, attestations_sha256}`，重命名 / 改字段 / 加事件 / 改 attestation 都会被 verifier 当场抓出来。

## 校验 audit-report

```bash
agent-lens-hook verify-audit-report audit-report.json \
  --pub ~/.agent-lens/keys/ed25519.pub
# OK · version agent-lens.dev/audit-report/v1 · 3 sessions · 17 events · attestations: 3 verified, 0 skipped
```

校验流程：
1. **Manifest re-hash**——Sessions / Attestations 字节重做 sha256，跟报告里的 manifest 比对。
2. **逐 session 链式校验**——每个 event 的 `prev_hash` 必须等于前一个 event 的 `hash`；`head_hash` 必须等于最后一个 event 的 `hash`。
3. **嵌入 attestation 重 hash**——envelope 字节重算 sha256 跟记录值比对。
4. **DSSE 验签**（可选）——给了 `--pub` 就逐条校验签名；省略 `--pub` 默认走 `~/.agent-lens/keys/ed25519.pub`，文件不存在则跳过 DSSE（manifest+chain 两步仍然走完）；显式 `--pub=` 强制跳过。

退出码：0 干净，1 发现 issue（链断 / hash 不匹配 / DSSE 验签失败），2 用法或文件错。这套语义跟 `verify-attestation` 一致，CD pipeline 直接 gate 在 exit 1 上。

## 校验单个 attestation

```bash
agent-lens-hook verify-attestation deploy.intoto.jsonl \
  --pub ~/.agent-lens/keys/ed25519.pub \
  --require-type agent-lens.dev/deploy-evidence/v1
# OK · payloadType application/vnd.in-toto+json · predicateType agent-lens.dev/deploy-evidence/v1 · keyid <id>
#   subject: ghcr.io/acme/widget (sha256:<digest>)
```

- exit 0：DSSE 签名通过，且（如果给了 `--require-type`）predicateType 一致。
- exit 1：验证失败——签名错、predicateType 不匹配、envelope 解码失败。CI 网关里挂这个 exit code 就能阻断未签名的部署。
- exit 2：用法 / 文件错（公钥读不到、文件不存在）。

`--pub` 默认 `$HOME/.agent-lens/keys/ed25519.pub`。

DSSE envelope 是标准格式，cosign / sigstore-go 都能识别——agent-lens 用同一份 ed25519 公钥（PEM/PKIX）签 + 验。把 `.intoto.jsonl` 的 payload (base64) 解出来再喂给第三方工具即可；后续若启用 Sigstore 网络模式（Fulcio + Rekor），`verify-attestation` 会扩展 `--rekor-url` 等 flag，envelope 格式不变。

## 校验哈希链

```bash
agent-lens-hook verify --session <session-id>
# OK · 6 events · head 7f53e1ebb9779555
# 或：FAIL at index 3 (id=01HXX...): prev_hash="...", expected "..."
```

v1 仅校验 `prev_hash → hash` 链路完整性，**不**重新从内容推导每条事件的 hash——后者需要 server 端重序列化逻辑（计划在后续 PR 加 server-side `verifyChain` GraphQL field）。当前足以发现"丢一条事件"或"链条被截断"的篡改，但敌手如能直接写库可伪造一条自洽的链。

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

## 接入部署系统（M3-A）

`/webhooks/deploy` 接收一种 generic JSON shape，K8s post-deploy job、Argo CD notification、Helm post-render hook、自定义 curl 都能用同一个端点。

```bash
curl -X POST https://<server>/webhooks/deploy \
  -H "Authorization: Bearer $AGENT_LENS_DEPLOY_WEBHOOK_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{
    "environment": "production",
    "git_sha": "deadbeefcafe1234567890abcdef0123456789ab",
    "image_digest": "sha256:abcdef0123456789",
    "image": "ghcr.io/acme/widget",
    "status": "succeeded",
    "deployed_by": "alice",
    "platform": "k8s",
    "cluster": "prod-us-east"
  }'
```

事件落到 session `deploy:<environment>`（例：`deploy:production`），refs 自动写 `git:<git_sha>` 和 `image:<digest>`。linker 把 deploy 跟之前的 commit / PR / build 串起来。

| 字段 | 必填 | 说明 |
|---|---|---|
| `environment` | ✅ | 决定 session_id（按环境分组所有部署历史） |
| `git_sha` 或 `image_digest` | 至少一个 | 决定 refs，linker 串接的入口 |
| `Idempotency-Key` header | 推荐 | 做 server 端 dedup；不传则每次都新事件 |

Token 配置：在 server 端设 `AGENT_LENS_DEPLOY_WEBHOOK_TOKEN=<random>`（与 `AGENT_LENS_TOKEN` **分离**，便于给部署系统最小权限）。未设则 `/webhooks/deploy` 返 503。

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

## §17 自观测（Dogfooding）

M3 收尾后激活：本仓库自身的开发循环跑在 agent-lens 上——prompt / thinking / tool call / commit 全链路落库，串成"工具用自己审计自己"的证据链。激活是**强 opt-in**，不会动外部贡献者的 home dir，也不在 Claude Code 里产生噪声。

### 一键激活

```bash
script/install-dogfood.sh
```

干三件事：

1. `go install ./cmd/agent-lens-hook`，把 hook 二进制塞进 `$GOBIN`。
2. 复制 `.claude/settings.example.json` → `.claude/settings.local.json`（gitignore 命中，per-developer），让 Claude Code 钩子开始转发到本机服务。
3. 写 `.git/hooks/post-commit`（per-clone，不进 git），commit 时自动上报 kind=commit 事件。

opt out：`rm .claude/settings.local.json .git/hooks/post-commit`。

### 跑本机服务

```bash
# 内存模式：零依赖，事件随重启清空，适合试水
AGENT_LENS_STORE=memory go run ./cmd/agent-lens

# 持久模式：Postgres + MinIO（本仓库 deploy/compose 里有现成 compose stack）
docker compose -f deploy/compose/docker-compose.yml up -d
go run ./cmd/agent-lens
```

默认端口 `:8787`，hook 默认走 `http://localhost:8787`。改动用 `AGENT_LENS_ADDR` / `AGENT_LENS_URL`。

### 验证证据链

激活后跑几轮 Claude Code，然后：

```bash
# 列你的 Claude session（hook 会用 claude-code:<uuid> 作 session_id）
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"query":"query { sessionHead(sessionId: \"claude-code:<uuid>\") }"}' \
  http://localhost:8787/v1/graphql

# 哈希链校验
agent-lens-hook verify --session claude-code:<uuid>

# 整条链路打成审计报告
agent-lens-hook export audit-report --root <event-id> --out my-trace.json
agent-lens-hook verify-audit-report my-trace.json
```

### 范围与限制

- **服务挂了 ≠ 不上报**：transport 默认在 POST 失败时把 NDJSON 落到 `~/.agent-lens/sessions/<sid>.ndjson`（per-session append，0600）。这是 §13 离线/弱网兜底机制，不是"明示静默"——所以 opt-in 后即便没起 agent-lens 服务，hooks 仍会在 home dir 累积事件文件。要彻底停活：先 `rm .claude/settings.local.json .git/hooks/post-commit`，再 `rm -rf ~/.agent-lens/sessions`。
- **不回填**：v1 自观测从激活日起算，激活前的 Claude Code 会话不导入。
- **redaction**：thinking 文本里可能含未脱敏的代码片段；在 hook 出口处做的截断按 §12 默认走，敏感内容上链前请自行 review。
- **GitHub webhook 回路**：要把 PR / push / workflow_run 也接进来，需要把本机服务通过隧道（ngrok / cloudflared）暴露到公网，再在 `dong-qiu/agent-lens` 仓库设置里挂 webhook。这一步 SPEC §17 没强制；按需做。
- **Project 模型**：v0 没有"项目"抽象，session_id 前缀（`claude-code:` / `github-pr:` / `deploy:` ...）已经天然分隔。多项目共用一个 store 时再补 project 维度。

## 许可

Apache License 2.0。详见 [`LICENSE`](./LICENSE)。
