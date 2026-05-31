# ADR 0013:订阅 PreCompact / PostCompact,把 compaction 从 inferred 提到 observed

- 状态:Accepted
- 日期:2026-05-29
- 取代:—(精化 ADR 0005 D5 的 compaction confidence 阶梯,见 § 后果)

## 背景

ADR 0005 D1 立了 `context_transform.compaction`,但 D5 给的采集只有三档:transcript 标记(疑似可见)、token-budget 启发式(inferred、假阳)、等 §10.4 proxy。0005 写作时**未考虑 `PreCompact` / `PostCompact` hook**——它们是 harness 在压缩**前 / 后**触发的一手信号,带 `trigger`(manual / auto),能把 compaction 从 inferred 直接提到 observed,且完全不需要 §10.4。这是 0005 决策当时信息不全留下的缺口。

§10.1 "已知局限" 当前写的是"compaction 首版按 token-budget 启发式推断,置 `loss_hint.confidence = inferred`"。本 ADR 用现成的 `PreCompact` + `PostCompact` 把这条局限缩小。

> 本 ADR 与 ADR 0012(SessionEnd)同源拆分。两者均为"Claude Code 提供了 hook、我们没订阅"的生命周期盲区,但接受时机不同:0012 复用既有 `decision` marker、可独立接受;本 ADR 依赖 ADR 0005 的 `context_transform` EventKind 先落地。早期草案曾借用 0012 `SessionStart.source=compact` 作"后括号";核对官方文档后发现存在专门的 `PostCompact` hook(更直接的后括号),故改用之,**本 ADR 不再依赖 0012**(详见 D2)。

## 验证

Claude Code 官方 hooks 文档(https://code.claude.com/docs/en/hooks,2026-05-29 抓取核对,以原始页面为准):

| Hook | 触发时机 | 文档化 payload / matcher | 可否 block |
|---|---|---|---|
| `PreCompact` | context 压缩**之前**(`/compact` 手动或自动) | `trigger`(manual / auto)+ **`custom_instructions`**(`manual` 携带用户传给 `/compact` 的内容;`auto` 为空) | 可(exit 2 / `decision:block`) |
| `PostCompact` | context 压缩**完成之后** | `trigger`(manual / auto);结构同 `PreCompact` | 不可(纯 side-effect) |

关键观察:

- `PreCompact` 文档化 payload **包含 `custom_instructions`**(`manual` 带内容、`auto` 空)——故 D3 据实抓取并脱敏,**不**作为待定字段处理。
- `PreCompact`(压缩前)与 `PostCompact`(压缩后)形成 compaction 的"前…后"括号,二者均带 `trigger`,可交叉确认。
- 两个 hook 都**不带压缩产物(summary)**——summary 仍归 ADR 0005 D2 由 transcript 旁路在 `PostCompact` 之后补采。

**仍未决、落地前必须抓样核对**:

- 自动 compaction 是否**一定**先发 `PreCompact`、后发 `PostCompact`(若 harness 某些路径跳过 `PreCompact`,则 0005 D5 的 token-budget 启发式仍需作为 fallback 兜底)。
- `PreCompact` 触发后、`PostCompact` 触发前进程崩溃 / 被 kill 时,记录形态(见 D1 的 provisional / confirmed 处理与 §后果 的崩溃缺口)。
- `PostCompact` 之后 transcript 中 summary 的实际落点形态(供 0005 D2 补采定位)。

落地 PR 按 ADR 0002 / 0005 同款记录覆盖度:用一份发生过自动 compaction、以及一次手动 `/compact` 的真实 session 核对上述三项。

## 决定

### D1. `PreCompact` 派生 provisional 的 `context_transform.compaction`,`PostCompact` 确认升 observed

`buildEvents` 增 `PreCompact` 分支,按 ADR 0005 D1 的 `context_transform` shape 填:`sub_kind=compaction`、`applied_at=PreCompact ts`、`triggered_by` 由 `trigger` 映射(`manual → user_explicit`、`auto → harness_auto`)。

confidence 分两步,避免"压缩开始即断言压缩完成":

- `PreCompact` 触发时事件标 **`loss_hint.confidence = provisional`**(压缩已开始、是否完成未知)。
- 配对的 `PostCompact`(D2)到达后,linker 把该 compaction 升 **`confidence = observed`**。
- 若 `PreCompact` 后始终无配对 `PostCompact`(进程在压缩中崩溃),事件停在 `provisional`——审计端据此知道"压缩开始过但未确认完成",不被误读为一次干净的 observed 压缩(见 §后果)。

`PreCompact` 拿不到 `after.summary_text`——summary 仍按 0005 D2 由 transcript 旁路在 `PostCompact` 之后补采,二者以 `session_id` + 时间窗关联拼成完整的 before/after。本 ADR 只负责"compaction 发生了、何时、谁触发、是否完成"的锚点;summary 文本的内容寻址 / redaction 仍归 0005 D2 / D6。

**否决备选**:只靠 0005 D5 的 token-budget 启发式。inferred、假阳率随长 session 升高;`PreCompact` / `PostCompact` 是免费的一手信号,没理由不用。它不删除 D5——D5 降级为"两 hook 未触发 / 未装时的 fallback"(见 § 后果)。
**否决备选**:`PreCompact` 触发即标 `observed`。压缩可能中途失败 / 崩溃,`PreCompact` 只证明"压缩将开始",不证明"压缩完成";provisional→observed 两步把这个区别如实表达。

### D2. 订阅 `PostCompact` 作 compaction 的后括号与确认信号,**取代借用 0012 的方案**

`PostCompact` 在压缩完成后触发,是比 `SessionStart.source=compact` 更直接的"后括号":它与同 `session_id` 的 `PreCompact`(D1)按 `trigger` + 时间窗配对,界定 compaction 的精确时间区间、把事件从 provisional 升 observed,并作为 0005 D2 transcript 旁路补采 summary 的触发时点。

采用 `PostCompact` 后,**本 ADR 不再依赖 ADR 0012**;`SessionStart.source=compact`(0012)仍可作辅助佐证,但不是本 ADR 的必需前置。

**否决备选**:沿用早期草案借 0012 的 `source=compact`。它在 0012 落地前不可用,且语义间接(借另一 hook 的字段推断"刚压缩过");`PostCompact` 是专门的一手后括号,直接、无跨 ADR 依赖。

### D3. `PreCompact.custom_instructions` 过 redaction 入 payload

`custom_instructions` 是文档化字段:手动 `/compact` 携带用户传入的指令、auto 为空。它记录"人对这次压缩的显式意图",属审计相关。非空则过 redaction(与 ADR 0005 D6 的 summary 同档,可能含语义 / PII)后入 `payload.before.compact_instructions`;为空则不发该字段。

**否决备选**:丢弃 `custom_instructions`。它记录了人主动塑形 context 的意图,丢了就少一条"为什么这次 context 被这样重写"的线索。
**否决备选**:把它当待定字段、抓样确认前不发(早期草案据二手摘要误判该字段不存在而采此路)。官方原始页面已确认字段存在并含 manual/auto 语义,故据实抓取;以原始文档为准,不据摘要降级。

### D4. 纯观测、绝不 block、fail-soft

`PreCompact` 文档上可 block(exit 2 / `decision:block`),但 agent-lens hook **绝不 block**——纯观测,一律 `os.Exit(0)`,与现有所有 hook 分支一致(`claude.go:80`)。`PostCompact` 本就不可 block。解析失败 warn 到 stderr,不中断 Claude Code 的压缩流程。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:**不变**——复用 ADR 0005 的 `context_transform` EventKind(其落地仍挂 0005)。`loss_hint.confidence` 增 `provisional` 值(payload 内字符串,非 proto enum);该值同样登记到 0005 的 confidence 集合(见 §后果)。
- `cmd/agent-lens-hook/setup.go`:hook 订阅列表增 `PreCompact`、`PostCompact`。
- `cmd/agent-lens-hook/claude.go`:`buildEvents` 增 `PreCompact` / `PostCompact` 分支;`custom_instructions` 走既有 `redactText` 出口。
- `internal/linking/`:compaction 前后括号关联(PreCompact ↔ PostCompact),provisional→observed 升级,以及 ↔ transcript summary 补采。
- GraphQL + Lens UI:compaction 节点显式区分 `provisional`(开始未确认)/ `observed`(已确认完成)。

**本 ADR 文档本身不带代码改动;落地实现与接受同 PR(见 § 落地)。**

## 后果

- §10.1 Hook 直采列表增 `PreCompact`、`PostCompact`;"已知局限" 的 **Compaction 边界** bullet 改写——从"首版 token-budget 启发式 inferred"升为"首版 `PreCompact`(provisional)+ `PostCompact`(确认 observed),summary 仍由 transcript 旁路补采;两 hook 未触发时回落 0005 D5 启发式"。
- §7 `EventKind` 不变(复用 0005 `context_transform`)。
- §15:R8("compaction 仅能启发式探测,精确建模等 §10.4")**缩小**——compaction 触发已 observed,仅 summary 内容的精确捕获仍部分依赖 transcript / §10.4。**另加一条已知局限**:`PreCompact` 触发后进程崩溃 / 压缩中断时,无配对 `PostCompact`,compaction 事件停在 `provisional`——表示"压缩开始过、未确认完成",镜像 ADR 0012 对 `SessionEnd` 崩溃缺口的处理。
- 与 ADR 0005 的关系:**精化 D5 的 confidence 阶梯**,并给 D1 的 `loss_hint.confidence` 集合增 `provisional` 值(observed 与 inferred 之间的"开始未确认"档)。0005 D5 把 observed 路径留给"等 §10.4 proxy",本 ADR 用 `PreCompact` / `PostCompact` 把 observed 提前到 §10.1 hook 路径;D5 的 token-budget 启发式从"首版主路径"降为"fallback"。不取代 0005 D1 的 shape、D2 的 summary 存储、D6 的 redaction。`修订(待 Accepted)` 的 SPEC 改动落地时,同步在 ADR 0005 的 confidence 集合注记 `provisional`。
- 与 ADR 0012 的关系:改用 `PostCompact` 后**已解除对 0012 的依赖**(D2);两份 ADR 现可完全独立接受,无依赖方向。
- 重放幂等:`PreCompact` / `PostCompact` 经 hook transport 的 at-least-once 通道(NDJSON fallback + `replay`)可能重复投递,与既有 hook 事件通性一致;compaction 事件带 `applied_at` + `trigger` + 时间窗,可在 linker 侧按 (session_id, triggered_by, 时间窗) 配对/去重。`context_transform` EventKind 已随 #131(Phase 0)落地;provisional↔observed 的配对收敛(把一次 compaction 的前后两条事件合成一条逻辑记录)归 `internal/linking` 后续。
- 数据量:`compaction` 每 session 0–N 条(长 session 多次),observed 模式下比 0005 D5 inferred 模式更少假阳。整体仍远低于 tool_call。
- 显式留给后续:compaction summary 文本的 observed 捕获(本 ADR 只锚前后时点,内容仍 transcript 旁路)、§10.4 proxy 下 compaction 全貌——归 ADR 0005 / §10.4 各自的修订,本 ADR 不越权。

## 落地

实现与接受同 PR:`PreCompact` / `PostCompact` 订阅(setup.go)+ `buildEvents` 两个分支(claude.go)——`PreCompact` 发 `context_transform.compaction`(`confidence=provisional`、`triggered_by` 由 `trigger` 映射、`before.compact_instructions` 过脱敏),`PostCompact` 发确认事件(`confidence=observed`),actor=system。`internal/linking` 的 provisional↔observed 配对收敛、以及 0005 D2 的 summary 补采,为后续子项。
