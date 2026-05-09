# Agent Lens v0.1.0 — 个人模式首发

第一次对外发布。Personal mode only —— 一个开发者在自己笔记本上跑、自己看自己的数据。

把你和 Claude Code 的每一次对话——prompt / thinking / tool call / tool result / 最后落地的 commit / PR / build / deploy——串成一条 hash-linked、可签名校验、可整条打包导出的事件链，存在你自己的笔记本上。多数 Coding Agent 都是黑盒；这是给那个黑盒装一面镜子。

## v0.1 你能做什么

- **Claude Code 全链路捕获**（`agent-lens-hook setup --personal` 一行装好）
  - prompt / thinking（含 `🚫 N thinking redacted` 占位指示器，标识 Claude Code 端丢弃的思考块）
  - tool call / tool result（Bash / Edit / Write / Read 等 + Agent 派发标识 `🤖 agent <id>`）
  - per-message token usage（input / output / cache_read / cache_write_5m / cache_write_1h，带 vendor 字段，跨厂商可比）
  - Claude Code 子 agent（Task tool）独立 session 捕获
- **Hook 端规则脱敏**（SPEC §12 路线第一步）
  - 7 类高置信度模式：PEM 私钥 / AWS / GitHub / Anthropic / Slack / HTTP Authorization / keyword=value secret
  - UI `🔒 N secrets redacted` chip，落库前替换为 `[REDACTED:<type>]`
- **GitHub webhook 接 PR / push / workflow_run**（可选；本机服务暴露公网才能用）
- **Lens Web UI**（embed 在 server binary 里，访问 server 同端口 :8787 即可）
  - Timeline 视图（最多 5000 事件 / session）
  - 因果图（ReactFlow + Monaco diff，单 session 上限 200 节点）
  - Sessions list + 每 session token 总用量
- **签名 attestation 三件套**（in-toto / SLSA / 自定义 predicate）
  - `code-provenance / slsa-build / deploy-evidence`
  - DSSE 信封 + 项目自有 ed25519 签名
- **审计报告导出**：以 deploy / commit / PR 为根，BFS 打包整条证据链 + 嵌入 attestation，单文件 JSON 自鉴
- **CLI 校验**：
  - `agent-lens-hook verify`（哈希链）
  - `agent-lens-hook verify-attestation`（DSSE 签名）
  - `agent-lens-hook verify-audit-report`（整包重 hash）
- **silent-degradation 自报**：hook HTTP 失败回落 NDJSON 时，stderr 一行可见警告 + `agent-lens-hook replay` 命令把 backlog 推回 server

## 安装

（实际下载链接在 GitHub Releases 上线后填具体 URL，下面是 ship 后的 README 引导格式）

### 60 秒快速试用

```bash
# 1. 拉镜像 + 起 PG + MinIO + agent-lens
docker run -d -p 8787:8787 ghcr.io/dong-qiu/agent-lens:v0.1.0
# 注：完整 stack（含 Postgres / MinIO 持久存储）走下面 docker compose 路径

# 2. 装 hook 二进制（Mac arm64 示例）
curl -fsSL https://github.com/dong-qiu/agent-lens/releases/download/v0.1.0/agent-lens-hook-darwin-arm64 \
  -o /usr/local/bin/agent-lens-hook
chmod +x /usr/local/bin/agent-lens-hook

# 3. 一键配 Claude Code hook + 起持久 stack
agent-lens-hook setup --personal

# 4. 浏览 http://localhost:8787 看 Lens UI
```

### 验证下载（可选）

v0.1.0 的二进制由项目自己的 ed25519 签发。下载下来可以用 v0.1 的 verify 工具校验 v0.1 的二进制：

```bash
curl -fsSL https://github.com/dong-qiu/agent-lens/releases/download/v0.1.0/agent-lens-public.pem -o pubkey.pem
agent-lens-hook verify-attestation \
  --pub pubkey.pem \
  --require-type "agent-lens.dev/release-artifact/v1" \
  agent-lens-hook-darwin-arm64.sig
# OK · payloadType application/vnd.in-toto+json · keyid <id>
```

`cosign` 用户走 README §Cosign 兼容性 段的 recipe（已实测）。

### 改源码 / 贡献

```bash
brew install go pnpm node              # 不再需要 golang-migrate（server 自迁移）
git clone https://github.com/dong-qiu/agent-lens
cd agent-lens
make compose-up                        # 起 PG + MinIO
make build-prod && ./bin/agent-lens    # build + 嵌入 UI 跑
```

详细见 README 主文档。

## v0.1 故意没做什么

透明工具该自己交代 scope。v0.1 不做：

- **团队多用户 / 数据隔离**：单机单人；多人共享 server 看得到彼此 prompt——这不是设计目标。Team mode 在 v0.2 路线，需要先有 ADR 决定"谁能看到谁"的 isolation 模型。
- **PR Review Bot outbound**：架构图里画了，§14 M2 注记里也明说"未交付"。需要先单开 ADR 决定 LLM 自动审 vs 规则引擎 vs 仅附 audit-report 链接 vs 与 §13 self-hosted 兼容方式。
- **采集器自身的 capture-time attestation**：当前能 verify "事件序完整"，不能 verify "事件源没被静默篡改"。ADR 0003 在补这个。
- **OpenCode / Cursor / 其他 AI CLI 接入**：v0.1 only Claude Code（SPEC §10.1）。OpenCode 在 §10.2 路线（v0.2）。
- **成本估算**：只记 token 数，不算 USD（vendor pricing volatile，写死容易过时；ADR 0002 D1 决定）。
- **Sub-agent 父→子自动 link**：父侧完整捕获、子 session 独立捕获，但父→子 `delegates` 自动连边留 v0.2（详见 ADR 0008 + 追踪 issue #85）。
- **Windows 二进制**：darwin / linux only。
- **Helm chart**：`deploy/helm/` 留空。v0.1 仅 Docker Compose。

## §17 自审 / Dogfood 故事

v0.1.0 的二进制由项目自己的 ed25519 签发；签发用的工具就是 v0.1 提供给你的 `agent-lens-hook export` + `verify-attestation`。**用 v0.1 校验 v0.1**——见上方"验证下载"段。

v0.1 的开发本身也是 dogfood：本仓库的开发循环全程跑在 agent-lens 上，从 v0.1.0 任何一次 commit 都能反向 trace 到触发它的 prompt 和 thinking。这是 §17 自观测从激活日（M3 收尾）一直跑到 v0.1.0 cut 的产物——这次发布的 release notes 自身的撰写过程也在 audit chain 里。

## 已知限制（装之前应该知道）

- **Hook 失败时落本地 NDJSON 文件**：`~/.agent-lens/sessions/<sid>.ndjson`。即便没起 server，hooks 仍会在 home dir 累积事件文件。stderr 一行警告会提示。彻底停活：删 `.claude/settings.local.json` + `~/.claude/settings.json` 的 hook 段（`agent-lens-hook setup --uninstall`）+ `rm -rf ~/.agent-lens`。
- **不回填**：`setup --personal` 之后的 session 才入库；之前的 Claude Code 历史不导。
- **Thinking 文本可能含敏感片段**：默认 redaction 抓 7 类高置信度密钥，**不抓 PII** / 不抓 entropy-based 通用秘密 / 不抓 Stripe / OpenAI / GCP / Discord / JWT 等其他 vendor。导出 audit-report 前自己 review。
- **`agent-lens verify` v1 弱**：只校验 `prev_hash → hash` 链路完整性，不重算 hash。敌手能直接写库就能伪造自洽链。强校验在 v0.2 路线。
- **Graph 视图最多 200 节点**：超过会卡（dagre + ReactFlow 渲染上限）；Timeline 视图无此限制（最多 5000）。Graph 是高层 summary 视图，深 drill-down 用 Timeline。
- **Sub-agent（Task tool）父→子手动关联**：v0.1 父侧捕获含 agentId、子 session 独立捕获，但**没自动连边**。审计时通过 timestamp + prompt 文本人眼对应。详见 ADR 0008。
- **Cosign 部分兼容**：`cosign verify-blob-attestation` 在 release-artifact 上原生工作（sha256 subject）；在 code-provenance / deploy-evidence 上需走手动 PAE recipe（README §Cosign 兼容性 有完整命令）。SLSA build 路径理论可走但 v0.1 未端到端验证。

## 下一步（v0.2 重点 / 无 ETA）

按当前 issue 跟踪：

- **Sub-agent 父→子自动 link**（[#85](https://github.com/dong-qiu/agent-lens/issues/85)）：候选 4 条路径（Claude Code 上游改 / FS marker / 时间相关启发 / hybrid）
- **Server-side ULID dedup**（[#81](https://github.com/dong-qiu/agent-lens/issues/81)）：让 replay 重跑安全
- **Cosign 兼容性升级**（[#75](https://github.com/dong-qiu/agent-lens/issues/75)）：sha256 alias 或 cosign-format 助手
- **Agent-lens-hook status 子命令**（隐式 #71 follow-up）：检查 fallback 队列大小
- **Web 测试基础设施**（[#62](https://github.com/dong-qiu/agent-lens/issues/62)）：vitest setup
- **Session.totalUsage N+1 + 全量 load 性能**（[#65](https://github.com/dong-qiu/agent-lens/issues/65)）
- **GraphQL TokenUsage 集成测试**（[#66](https://github.com/dong-qiu/agent-lens/issues/66)）
- **Agent transparency follow-ups**（[#58](https://github.com/dong-qiu/agent-lens/issues/58)）

更广 v0.2 主题：team mode / capture-time attestation（ADR 0003）/ human-intervention events（ADR 0004）/ context transform events（ADR 0005）/ Helm chart / OpenCode 接入 / §10.4 proxy 深模式（实时拦截 + 流式 thinking）。

## 反馈

GitHub Issues。dogfood 自己用 v0.1 抓自己的开发证据，发现 patterns 后开 issue 是最直接的路径。

---

**v0.1.0 release engineering note**：本 release 的 8 个 binary（agent-lens × agent-lens-hook × darwin/linux × amd64/arm64） + container image（multi-arch GHCR）+ checksums 都由 release.yml 在 tag push 时自动产出 + 签名（ADR 0006 D6 / D7）。第一次 tag 失败的迭代成本是删 tag / 修 / 重 tag，不毁灭性——所以你也许会看到 v0.1.1 出现得比较早。
