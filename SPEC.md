# Agent Lens — 项目 SPEC

> 版本：v0.5（2026-04-28）
> 状态：草案 / 规划阶段
>
> v0.5 变更：把每轮交互的 token 用量纳入证据链。撤回 v0.4 §10.1/§17 关于"hook 路径不抓 token / stop_reason"的限制（已被 transcript 验证证伪）。**价格 / 费用估算明确排除在 v1 范围之外**——多厂商计费结构差异大,token 数才是审计相关原语。详见 `docs/ADR/0002-token-usage-and-cost.md`。

## 1. 项目愿景

Agent Lens 是面向 Coding Agent 的**可观测、可追溯、可审计**系统。它捕获开发者与 Coding Agent 协同开发的全过程——从需求、Prompt、Agent 内部推理、工具调用，到代码、测试、构建、部署——并将所有事件串联成一条**可验证的证据链**，让"这行代码为什么进了生产"这类问题有据可查。

类比：Agent Lens 之于 AI 辅助研发流程，类似 DataDog/Honeycomb 之于运行时系统。

## 2. 目标

- **G1 全程捕获**：覆盖 Prompt → Agent Thinking → Tool Calls → Code Change → Test → Build → Deploy 的每一类事件。
- **G2 因果串联**：把跨阶段、跨工具、跨 Agent 的事件按因果与时序拼成可查询的 trace。
- **G3 防篡改**：事件 append-only，哈希链 + 可选签名，可校验、可重放。
- **G4 可审计**：支持按时间、人、Agent、需求 ID、commit、PR、build、deploy 任意维度回溯。
- **G5 低侵入接入**：通过 hook / SDK / Git hook / CI 插件接入，不要求改造既有工作流。
- **G6 供应链兼容**：在阶段边界输出符合 in-toto / SLSA 规范的 attestation，可被 cosign / policy controller 直接消费。

## 3. 非目标

- 不做 Coding Agent 本身。
- 不替代 IDE / CI / CD / 项目管理工具（但能引用其中的实体 ID）。
- v1 不做实时干预（不充当 policy gate 阻断 Agent），只做记录与回溯。
- 不覆盖非编码类 Agent（如客服、销售）。

## 4. 用户角色

| Persona | 主要诉求 |
|---|---|
| 开发者 | 看 Agent 是怎么得出某段代码的，可回放、分享 |
| Reviewer / Tech Lead | PR review 时一键看到从需求到代码的完整推理链 |
| 审计 / 合规 | 证明某次生产变更的来源、推理依据与审批路径 |
| 工程经理 | 度量 Agent 使用模式、错误模式、人机分工演变 |

## 5. 核心概念

- **Session**：开发者与 Agent 的一次连续会话。
- **Turn**：一次 prompt + response 的来回。
- **Thought**：Agent 内部推理（reasoning、计划、自我纠错）。
- **ToolCall**：Agent 发起的工具操作（read / edit / bash / search / MCP）。
- **Artifact**：外部产出物（commit、PR、test run、build、image、deployment）。
- **Link**：事件之间的因果或语义关联。
- **Trace / Evidence Chain**：以某目标（如一次 deploy）为根，反向展开的全部上游事件。
- **Attestation**：对一段 trace 的签名声明，按 in-toto DSSE 信封格式输出。
- **TokenUsage**：单条 assistant 消息的 token 计量（input / output / cache_read / cache_write 5m / cache_write 1h / web_search / web_fetch + model + service_tier）。供应商无关 schema，vendor 字段标注来源。**v1 只记录 token 数，不计算费用**——多厂商计费结构差异大且易变,留给下游消费者按需自行计算。详见 ADR 0002。

## 6. 核心能力

1. **Capture**：CLI / IDE hook、Git hook、CI 插件、部署 webhook。
2. **Ingest**：标准化事件 schema（参考 OpenTelemetry，扩展 AI 字段）。
3. **Link**：基于 commit SHA、PR ID、session ID、需求 ID 跨阶段拼接。
4. **Store**：append-only 事件流 + 内容寻址的 artifact 存储。
5. **Query**：按 trace / 时间 / 人 / Agent / 文件 / 需求 ID 检索。
6. **Visualize**：时间线 + 因果图 + 代码 diff 关联视图（"Lens UI"）。
7. **Verify**：哈希链 + 签名 + replay。
8. **Export**：阶段边界输出 in-toto attestation（含 SLSA provenance predicate）。

## 7. 数据模型

```
Event {
  id: ULID
  ts: timestamp
  session_id, turn_id?
  actor: { type: human|agent|system, id, model? }
  kind: prompt | thought | tool_call | tool_result
       | code_change | commit | pr | test_run
       | build | deploy | review | decision
  payload: <kind-specific JSON>
  parents: [event_id]      // 因果上游
  refs:    [artifact_id]
  hash, prev_hash          // 哈希链
  sig?                     // 可选签名
}

Artifact {
  id: content_hash
  type: blob | commit | pr | image | test_report | ...
  meta: { ... }
  blob_ref: <storage path>
}

Link {
  from_event, to_event
  relation: produces | references | reviews | builds | deploys
  confidence: 0.0..1.0
  inferred_by: rule_id | manual
}
```

`payload.usage` 子对象（仅出现在由 assistant 消息派生的 `decision` / `thought` 事件，可选）：

```
TokenUsage {
  vendor:                  "anthropic" | "openai" | ...
  model:                   string                    // 厂商原始 model id
  service_tier?:           string                    // standard / priority / batch
  input_tokens:            int
  output_tokens:           int
  cache_read_tokens?:      int
  cache_write_5m_tokens?:  int
  cache_write_1h_tokens?:  int
  web_search_calls?:       int
  web_fetch_calls?:        int
  raw?:                    object                    // 原始 vendor usage 块，留作 forensic re-parse
}
```

v1 不计算 / 不存储费用。事件层面只承载原始 token 数,turn / session 级聚合在 query 层做。映射规则、跨厂商抽象、为何不做费用详见 ADR 0002。

## 8. 系统架构

```
[Claude Code hook] [OpenCode plugin] [Git hook] [GitHub App] [CI plugin] [Deploy webhook]
        \             |              |              |             |             /
         ──────────────────  Ingest API (gRPC + HTTP)  ──────────────────
                                       │
              ┌────────────────────────┼────────────────────────┐
              ▼                        ▼                        ▼
        Event Store              Artifact Store            Linking Worker
       (Postgres /              (S3-compatible /          (rules + heuristics)
        ClickHouse)              本地 OCI store)
              │                        │
              └────────┬───────────────┘
                       ▼
                 Query API (GraphQL)
                       │
        ┌──────────────┼─────────────────┐
        ▼              ▼                 ▼
    Lens Web UI    PR Review Bot     Attestation Exporter
                                     (in-toto / SLSA)
```

## 9. 关键流程

### 9.1 写代码
1. 开发者在 Claude Code 中发起 prompt。
2. Hook 把 prompt / thinking / tool_call / tool_result 事件流式推到 Ingest。
3. 编辑器写文件 → Git hook 在 commit 时上报 commit event 并 link 到本次 session 的 turn。

### 9.2 评审
1. PR 创建时 GitHub App 上报 pr event。
2. Linking worker 用 commit SHA 把 PR 关联到上游所有 turn。
3. PR Bot 在 PR 上评论一段"Evidence Summary"链接到 Lens UI。

### 9.3 构建与部署
1. CI 插件上报 build event；产物 hash 写入 artifact store。
2. Deploy webhook 上报 deploy event 并附 image digest。
3. Attestation Exporter 输出 in-toto attestation（predicate = SLSA provenance v1 + Agent Lens 自定义 predicate `agent-lens.dev/trace/v1`）。

### 9.4 回溯
**问题**：这行代码为什么进了 prod？
**链路**：Deploy → Build → Image SHA → Commit → PR → Turn → Prompt → 关联需求 ID。每一跳都有事件、时间戳、哈希。

## 10. 接入策略

### 10.1 Claude Code（首发）

**事件捕获路径**：
- **Hook 直采**（`SessionStart` / `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / `Stop`）：覆盖 prompt、工具调用与结果、会话/turn 边界。事件由 `agent-lens-hook claude` 子命令解析 stdin 并 POST 到 Ingest；Ingest 不可达时回落 `~/.agent-lens/sessions/<sid>.ndjson` 文件 sink，供日后 `agent-lens replay`。
- **Transcript 旁路**（`Stop` 触发时）：读取 hook payload 的 `transcript_path`，对自上次 cursor 起新增的 jsonl 行做增量解析，提取每个 assistant 消息的 `thinking` 与 `text` content block：
  - `thinking` block → `EVENT_KIND_THOUGHT`
  - `text` block → `EVENT_KIND_DECISION`，payload.marker = `assistant_message`
  - `tool_use` / `tool_result` block → 跳过（已由 PreToolUse / PostToolUse 捕获，避免重复）
  - cursor 持久化到 `~/.agent-lens/cursors/<sid>.offset`，仅在 transport 成功后推进
- 通过 `~/.claude/settings.json` 注册 hook，单一 `agent-lens-hook` 二进制按子命令分发。
- 可选 MCP server，把 Lens 查询能力反哺给 Agent。

**Thinking 捕获条件**：仅当 Claude Code 在该轮启用了 extended thinking，transcript 中才会有 `thinking` block 可读。本路径不主动开启该选项，也不强制其存在。

**Token 用量与 stop_reason 捕获**：transcript 中每条 assistant 消息的 `message.usage` 块（input / output / cache_read / cache_creation 含 5m+1h 分桶 / server_tool_use 含 web_search+web_fetch / service_tier）以及 `message.model`、`message.stop_reason` 均被 transcript 旁路在 Stop hook 中提取，归一化为 §7 `payload.usage` 的 `TokenUsage` shape，挂在该消息派生的 `decision` / `thought` 事件上。验证基础与映射规则见 ADR 0002。原 v0.4 版本曾把这些字段列为本路径不抓、推迟到 §10.4 代理深模式——经实测撤回。

**已知局限**：
- Claude Code transcript jsonl 不是公开稳定契约，解析按 fail-soft：未识别行跳过，不中断流。最低支持版本随发版迭代标注于 README。
- `<synthetic>` 模型标记的消息（Claude Code 自身注入的 stop-sequence 占位，usage 全 0）按已知形态跳过 usage 提取，不报错、不丢消息体。
- 仍**没有**的能力：实时拦截 / token 流式即时反馈 / policy gate。要这些得走 §10.4。

### 10.4 代理深模式（M4+，未启用）

为获取实时拦截能力（流式 thinking 与 token 即时反馈、policy gate），未来可让 Claude Code 走 `ANTHROPIC_BASE_URL` 指向本机 Agent Lens proxy，由 proxy 拦截请求/响应再转发上游。复杂度高于 §10.1，故 v1 不启用；激活条件由独立 ADR 拍板。注意：**token 总量统计**已通过 §10.1 transcript 旁路覆盖，不再是 §10.4 的独占价值。

### 10.2 OpenCode（次发）
- OpenCode 是开源 Coding Agent，预期通过其插件机制或 fork patch 注入 capture 层。
- schema 复用 Claude Code 路径，差异在 thinking 字段映射与 tool 命名空间。

### 10.3 通用 Capture SDK
- 提供语言无关（先 Go/TS）的薄 SDK，封装事件 schema、签名、批量上报。
- 后续接入 Cursor / Copilot / 自研 Agent 都基于该 SDK。

## 11. 供应链兼容（in-toto / SLSA）

姿态：**内部自定义 schema，阶段边界输出标准 attestation**（即"Option B"）。

- 内部 Event/Trace 用自定义细粒度 schema（流式、分钟级事件量友好）。
- 在以下阶段边界生成 in-toto DSSE attestation：
  - **commit**：predicate = `agent-lens.dev/code-provenance/v1`（含 prompt / thinking 摘要 + commit SHA + 关联 turn 列表）。
  - **build**：predicate = `slsa.dev/provenance/v1`（标准 SLSA build provenance）。
  - **deploy**：predicate = `agent-lens.dev/deploy-evidence/v1`（关联上游 trace 根 ID）。
- 签名使用 Sigstore（cosign / Fulcio / Rekor）或本地 KMS；自托管模式两者皆可。
- 对外定位：**把 SLSA / in-toto 的可信链路向 AI 协作上游延伸**，输出可被 cosign verify / Kyverno / Conftest 等现有工具直接消费的 attestation。

## 12. 安全与合规

- **数据敏感性**：prompt / thinking 可能含密钥、PII、专有代码。入库前 redaction（先规则式，后续可加模型辅助）。
- **存储**：默认本地 / 自托管；事件 append-only，artifact 内容寻址不可改名。
- **完整性**：每条 event 携带 prev_hash 形成哈希链；可选 RFC 3161 时间戳或 Rekor 透明日志。
- **访问控制**：RBAC，trace 可见性按项目 / 角色限定；审计访问日志自身也作为 event 入库。
- **合规删除**（如 GDPR）：以"标记 + 加密销毁内容"实现，保留事件骨架以维持哈希链。

## 13. 部署形态

**自托管优先。**

- **Single-node**：Docker Compose，含 Postgres、对象存储（MinIO）、Ingest、Query、Web UI。面向小团队 / 试点。
- **HA**：K8s Helm chart，事件存储可换 ClickHouse，artifact 存储用集群对象存储。
- **气隙**：所有依赖（含 redaction 模型）支持离线运行。
- **SaaS**：远期考虑，不在 v1 范围。

## 14. MVP 与里程碑

### M1（4–6 周）：闭环 demo
- Claude Code hook → Ingest → Postgres event store。
- Git post-commit hook → commit event。
- Lens UI 时间线视图：从 commit 反向展开到 prompt。
- Single-node Docker Compose 部署。

### M2（再 6–8 周）：跨阶段串联
- GitHub App：PR 事件 + PR Review Bot。
- GitHub Actions 插件：build 事件 + 产物 hash。
- Linking worker：基于 SHA / PR / branch 自动拼接。

### M3（再 6–8 周）：可验证
- Deploy webhook（K8s / Argo / 自定义）。
- in-toto / SLSA attestation 导出 + cosign 集成。
- 哈希链校验 CLI：`agent-lens verify`。
- 审计报告导出（PDF / JSON）。
- M3 完成即激活 §17 自观测，把本仓库自身后续开发作为首个 dogfood。

### M4：扩展
- OpenCode 接入。
- ClickHouse 后端选项。
- RBAC / 多项目。

## 15. 风险与开放问题

| # | 问题 |
|---|---|
| R1 | thinking 字段是否落盘？版权 / 隐私边界在哪？需要 product policy。 |
| R2 | linking 准确度——纯启发式 vs 强制显式 ID（如 commit message trailer）。 |
| R3 | 不同 Agent 的 thinking schema 不统一，归一化代价多大。 |
| R4 | 私有代码的存储成本与保留策略：只存 diff 还是全文件 snapshot？ |
| R5 | 与现有 OpenTelemetry-GenAI / Langfuse 等观测系统的关系：互补还是重叠？ |
| R6 | attestation 的密钥管理：自托管下用本地 KMS 还是 Sigstore（需外网）？ |
| R7 | 跨厂商 TokenUsage 可比性：OpenCode / Cursor / 自研 Agent 的 usage schema 不一致——尤其 cache 语义(Anthropic 是 TTL 分桶 + 写入按倍率,OpenAI 是缓存输入打折)无法用同一字段名表达。SDK 层定义最小公约数 `TokenUsage`(input / output 通用,cache 字段按需扩展),vendor 字段保留出处,聚合时按 vendor 分组而非强行求和。详见 ADR 0002 D2。 |

---

> 下一步：M1 立项 → 写 Claude Code hook 原型 → 打通 commit → trace → UI 闭环。技术栈已锁定，详见 §16。

## 16. 技术栈与仓库布局

技术栈决定见下表，理由概述见 §16.2，详细对比与备选记录在 `docs/STACK.md`（待补）。

### 16.1 技术栈决定

| 层 | 选择 | 备选 / 演进 |
|---|---|---|
| 后端语言 | **Go** | Rust 已评估，因 in-toto/SLSA 生态重力放弃 |
| 后端 Web 框架 | `chi` 或 `echo`（轻量）+ 标准库 | — |
| Schema 定义 | **Protobuf**（`buf` 管） | canonical schema，派生 Go / TS 类型 |
| Ingest 协议 | **HTTP + NDJSON**（M1–M3） | M4+ 视情况增加 gRPC ingest 通道 |
| Query 协议 | **GraphQL**（`gqlgen`） | + 最小 REST 给程序化集成 |
| 事件存储 | **Postgres**（`pgx` + `sqlc` + `golang-migrate`） | M4+ 通过 CDC 同步 ClickHouse 做分析副本 |
| 对象存储 | **MinIO**（S3 协议） | 云上换 AWS S3 / GCS / R2 |
| 签名 / Attestation | **sigstore-go** + 本地 ed25519 密钥 | 联网模式可启用 cosign + Fulcio + Rekor |
| Hook 二进制 | 单个 Go 二进制 `agent-lens-hook`（多子命令） | — |
| 前端框架 | **React + TS + Vite** | — |
| 前端样式 / 组件 | **Tailwind + shadcn/ui** | — |
| 因果图 | **ReactFlow** | — |
| 代码 / Diff | **Monaco Editor** | — |
| 前端数据层 | **TanStack Query** + GraphQL Codegen | — |
| 容器化 | Docker buildx 多架构镜像（amd64 + arm64） | — |
| 本地编排 | Docker Compose（M1） | M3+ 提供 Helm chart |
| 自身可观测 | OpenTelemetry SDK + Prometheus + slog | — |
| 包管理 | Go modules / `pnpm` | — |
| Lint / 格式 | `golangci-lint` / `eslint` + `prettier` | — |

### 16.2 关键决策理由

1. **Go 而非 Rust**：§11 的 in-toto / SLSA 工具链原生 Go（in-toto-golang、sigstore-go、cosign、slsa-verifier），同语言能省下大量 wrapper 与气隙打包成本。Go 的迭代速度与云原生生态厚度也更利于 MVP。Rust 的硬优势（性能、内存安全）在我们的 ingest 规模下不能兑现。
2. **HTTP/NDJSON 而非 gRPC**：hook 作者用 `curl` 即可上报，调试可读，代理/防火墙友好。我们预估的事件量级（单开发者峰值 ~50 events/s）远未到 gRPC 才能解决的尺度。protobuf canonical schema 保留，将来加 gRPC 通道是叠加而非重写。
3. **Postgres 而非 ClickHouse（M1–M3）**：哈希链需要严格全序写入；ACID + JSONB 对自托管小团队最易运维。CH 在 M4+ 作为分析副本通过 CDC 同步即可。
4. **React 而非 Svelte/Solid**：Lens UI 重依赖 ReactFlow（因果图）+ Monaco（代码 diff），这两个组件的生态优势压倒框架本身的性能差。
5. **本地 ed25519 而非默认 Sigstore**：自托管 / 气隙是首要目标，Sigstore 需外网（Fulcio / Rekor）。sigstore-go 库本身两种密钥源都支持，所以不锁定，只切换默认。

### 16.3 仓库布局

```
agent-lens/
├── proto/                  # Protobuf canonical schema（事件 / artifact / link）
├── cmd/
│   ├── agent-lens/         # 主服务（ingest + query + linking worker）
│   └── agent-lens-hook/    # 客户端 hook 二进制（claude / git / verify / export 子命令）
├── internal/
│   ├── ingest/             # HTTP/NDJSON ingest handler
│   ├── store/              # Postgres + S3 持久化
│   ├── linking/            # 跨阶段事件关联规则
│   ├── query/              # GraphQL resolver
│   ├── attest/             # in-toto / SLSA attestation 生成
│   ├── redact/             # 敏感字段脱敏
│   ├── hashchain/          # 哈希链 + 签名 + 校验
│   └── auth/               # RBAC / token
├── web/                    # React + Vite 前端
│   ├── src/
│   ├── public/
│   └── package.json
├── deploy/
│   ├── compose/            # M1 docker-compose.yml
│   └── helm/               # M3+ K8s Helm chart
├── migrations/             # SQL（golang-migrate）
├── docs/
│   ├── STACK.md            # 详细选型记录与备选
│   └── ADR/                # Architecture Decision Records
├── scripts/                # 开发辅助脚本
├── SPEC.md
├── CLAUDE.md
├── README.md
├── go.mod
├── Makefile
└── buf.yaml
```

## 17. 自观测（Dogfooding，延后激活）

**目标**：Agent Lens 自身的开发过程作为**首个被观测对象**——把 prompt、tool call、commit、PR、build 全链路接入 Agent Lens，实现"工具观测工具自己的演进"。

**激活时机**：不早于 **M3 完成**。届时 `agent-lens verify`（哈希链校验）与 in-toto / SLSA attestation 导出已就位，自观测产生的证据链才具备可验证性；在此之前只是"日志的日志"，价值有限。

**对当前规划的影响**：**无**。M1–M2 的接入设计、UI、性能预算都按外部团队使用场景规划，不为 dogfood 引入特殊路径。气隙离线运行（含本地文件 sink）由 §13 统一覆盖，不为 dogfood 单独开口子。

**激活时要做的事**（占位清单，届时细化）：
- 在本仓库的 `.claude/settings.json` 注册 hook 指向本机 `agent-lens` 服务。
- 历史会话取舍：v1 自观测从启动日起算，不回填激活前的会话。
- Redaction 规则按 §12 默认策略走，不因 dogfood 放宽——thinking 文本尤其敏感，redaction 必须在 hook 出口处完成，不依赖 server。
- 在 Lens UI 上把"Agent Lens 自身"作为一个 project 单独建模，避免与未来其他被观测项目混淆。

**已具备的捕获深度**（截至 v0.5）：§10.1 的 hook 直采 + transcript 旁路使得激活后能拿到 prompt / 工具调用 / 工具结果 / thinking（启用 extended thinking 时）/ assistant 文本回复 / turn 边界 / **每条 assistant 消息的 token 用量与 stop_reason**（含 cache 5m/1h 分桶、server_tool_use 计数、service_tier、model）。仍要等 §10.4 代理深模式才能拿到的能力：实时拦截、流式 token 即时反馈、policy gate。

**反馈回路**：dogfood 暴露的可用性问题以 issue 入仓，作为 M4+ 优先级输入。
