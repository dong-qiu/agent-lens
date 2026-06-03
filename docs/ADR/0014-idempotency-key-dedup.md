# ADR 0014:用幂等键去重,与事件 id / 哈希链排序解耦

- 状态:草案
- 日期:2026-06-03
- 取代:—
- 修订(待 Accepted):SPEC §7(Event 增 `idempotency_key`)、§10.1(hook 重放幂等)

## 背景

`agent-lens-hook replay`(ADR 0006 N1 / PR #79)把 Ingest 不可达时回落的 `~/.agent-lens/sessions/<sid>.ndjson` 文件**重新 POST** 到 `/v1/events`。但**重放不可重复运行**:对同一文件 POST 两次会产生重复事件。issue #81 的实测:同一 NDJSON POST 3 次,事件数涨 3 倍——尽管 `events.id` 是 Postgres UNIQUE PRIMARY KEY,因为**每批都拿到新 ULID**。

根因:`internal/ingest/handler.go` 仅在 client **没给** id 时赋服务端 ULID(`if in.ID == ""`),而 **hook 的 `baseEvent` 根本不设事件级 `id`** —— 只填 ts / session_id / actor / kind / payload。于是 hook 事件每次入库都得新 ULID,重放即重复。GitHub webhook 路径把 `X-GitHub-Delivery` 设成 `id`、靠 PK 唯一去重,**hook 路径没有等价机制**。

这个去重缺口已被 ADR 0010 / 0011 / 0012 / 0013 的 § 后果反复写成"归 ingest 既有机制"——但**那机制对 hook 路径并不存在**,正是本 ADR 要补的。下一步的 linker 关联(把权限 gate、compaction 前后事件配对)在有去重时才安全。

## 验证

代码探查(2026-06-03,main):

- `internal/ingest/handler.go:181`:`if in.ID == "" { in.ID = ulid.Make().String() }`——保留 client id,但 hook 不提供 id。
- `cmd/agent-lens-hook/claude.go` `baseEvent`:返回的 map **无 `id` 键**。
- **排序与哈希链头都靠 `id`(ULID)**:
  - `internal/store/postgres.go`:`ORDER BY session_id, id ASC`(事件列表)、`ORDER BY id ASC LIMIT`(按 session)、`ORDER BY id DESC LIMIT 1`(`HeadHash`)。
  - 即**哈希链的"上一条" = 当前最大 id**。新事件入库时 prev_hash 取自该链头,故新事件的 id 必须比链头**大**,链才接得对。
- GitHub webhook `mapper.go`:`WireEvent.ID = deliveryID`,经 PK 去重。

**关键结论**:服务端单调递增 ULID 是**哈希链完整性载荷**。若改用 hook 自生成的 ULID 当 `id`(跨进程、系统时钟有偏移),新事件可能拿到比链头**小**的 id → 链头判定 / `verify` 出错。这与 issue #81 自己标注的"hook ULID 单调性对哈希链 load-bearing,改它会和 verify 打架"一致。本 ADR 因此触及 `internal/hashchain` 的完整性推理,接受 / 落地走**强制 `/review`**。

## 决定

### D1. `id` 继续由**服务端**生成 ULID;不把 client / hook 的 ULID 当 `id`

事件 `id` 保持服务端在 append 锁内赋值(现状),从而排序(`ORDER BY id`)与哈希链头(`HeadHash = max id`)的单调性不变、`verify` 不受影响。

**否决备选(#81 候选 A / B)**:保留 client ULID 当 `id` + 409 / `ON CONFLICT DO NOTHING`。把 hook 自生成的 ULID 塞进 `id`,就把哈希链头的单调性交给了跨进程、有时钟偏移的 hook 时钟——一条迟到事件拿到比链头小的 id,链就断。去重不该以牺牲完整性为代价。

### D2. 新增独立的 `idempotency_key`,由产出方计算,作为**唯一去重键**

`id` 管排序 / 链;`idempotency_key` 管去重——两者解耦。

- **hook**:`idempotency_key = sha256(canonical(session_id, ts, kind, actor, payload))` 的 hex。内容寻址,确定性:同一事件(含重放时从 NDJSON 读回的原序列化)算出同一键。
- **GitHub webhook**:`idempotency_key = X-GitHub-Delivery`(天然幂等令牌);不再把它塞进 `id`,`id` 也归服务端 ULID,全路径一致。
- **deploy webhook**:沿用其 `Idempotency-Key` 头(已有)。

**否决备选**:hook 路径加键、webhook 仍用 `deliveryID` 当 `id` 走 PK 去重——两套去重机制、`id` 语义不一(webhook 是 delivery、hook 是服务端 ULID)。统一到 `idempotency_key` 后 `id` 处处是"服务端排序锚",语义干净。

### D3. 服务端按 `idempotency_key` 幂等插入,返回真实 inserted 数,批量遇重**跳过续行**

- store 加 `idempotency_key` 列 + 唯一索引;插入走 `ON CONFLICT (idempotency_key) DO NOTHING`。
- `/v1/events` 的 `{accepted: N}` 返回**实际入库数**(非批大小),让重放/重试能看出"这次跳了几条重复"——回应候选 B"静默丢重可能掩盖 bug"的顾虑:不静默,如实计数。
- HTTP 批量处理遇已见键 → **跳过该条、继续**,不再 409 整批失败(现状 `writeAppendError` 把 dup 当 409 中断)。webhook 路径维持优雅 ack。

**否决备选**:dup 仍 409 整批失败(候选 A)。重放本就预期撞重,整批失败让"安全重跑"无从谈起;跳过续行才是幂等。

### D4. 键随 wire event 入 NDJSON fallback,重放原样带回 → 天然幂等

`idempotency_key` 是 wire event 的字段,Ingest 不可达回落文件 sink 时一并写入;`replay` 原样 re-POST，键不变 → 服务端跳重。键又是内容确定性的,即便某路径重算也一致。`replay --remove-on-success` 从"必须"降为"可选"。

### D5. 纯增量、不破坏既有数据

`idempotency_key` 列可空(老事件无键、不参与去重,保持现状);唯一索引对 NULL 不约束(Postgres 多 NULL 允许)。server 启动自迁移加列 + 索引(`AGENT_LENS_SKIP_MIGRATE` 之外)。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:`Event` 增 `idempotency_key`(string)→ `make proto`;`internal/ingest` `WireEvent` 同步。
- `internal/store`:events 表加 `idempotency_key` 列 + 唯一索引;`Append` 走 `ON CONFLICT DO NOTHING` 并回报是否实际插入;新 migration(server 自迁移)。
- `internal/ingest/handler.go`:批量遇 `ErrDuplicate`/skip 改"跳过续行 + 计数",`{accepted}` 返回真实 inserted 数。
- `cmd/agent-lens-hook`:`baseEvent` / transport 计算 `idempotency_key`(canonical 序列化 + sha256);写入 wire event 与 NDJSON fallback。
- `internal/webhooks/github`:`idempotency_key = deliveryID`,`id` 改服务端 ULID。
- `internal/query` GraphQL:`Event` 可选透出 `idempotencyKey`(审计 / 调试),非必需。

**本 ADR 不带任何代码改动。**

## 后果

- §7 `Event` 增 `idempotency_key` 字段;§10.1 增"hook 重放幂等"一句(`replay` 可安全重跑)。
- `verify` / 哈希链:**不变**——`id` 仍服务端单调 ULID,链头逻辑、`internal/hashchain` 推理原样。这正是选 C 而非 A/B 的理由。接受/落地仍走强制 `/review`(触及链完整性推理,即便结论是"不动")。
- 0010 / 0011 / 0012 / 0013 § 后果里"去重归 ingest 既有机制"的承诺**由本 ADR 兑现**:test_run 不双计通过率、permission/compaction 前后事件重放不重复。
- linker 关联(下一步)可安全假定"同一事件至多一条",前后配对不被重复污染。
- 数据增量:每事件一个 64 hex 字符的键列(~64 字节)+ 一个唯一索引。可接受。
- migration:加列 + 索引;老事件键为 NULL、不去重(保持现状,不回填)。
- 显式留给后续:跨产出方的全局去重策略(若未来引入第三类产出方)沿用同一 `idempotency_key` 契约,不需再决策。
