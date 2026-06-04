# ADR 0014:用每事件幂等键去重,与事件 id / 哈希链排序解耦

- 状态:Accepted + **Implemented**(2026-06-04,#81——见 § 落地 末「实现记录」)
- 日期:2026-06-03(接受)/ 2026-06-04(落地)
- 取代:—

## 背景

`agent-lens-hook replay`(ADR 0006 N1 / PR #79)把 Ingest 不可达时回落的 `~/.agent-lens/sessions/<sid>.ndjson` 文件**重新 POST** 到 `/v1/events`。但**重放不可重复运行**:对同一文件 POST 两次会产生重复事件。issue #81 的实测:同一 NDJSON POST 3 次,事件数涨 3 倍——尽管 `events.id` 是 Postgres UNIQUE PRIMARY KEY,因为**每批都拿到新 ULID**。

根因:`internal/ingest/handler.go:181` 仅在 client **没给** id 时赋服务端 ULID(`if in.ID == ""`),而 **hook 的 `baseEvent` 根本不设事件级 `id`**——只填 ts / session_id / actor / kind / payload。于是 hook 事件每次入库都得新 ULID,重放即重复。

**驱动**是重放幂等本身,不是别的:让 `replay` 安全可重跑、把 `--remove-on-success` 从"必须"降为"可选(对升级后的事件)",并兑现 ADR 0010 / 0011 / 0012 / 0013 § 后果反复写的"去重归 ingest 既有机制"——那机制对 hook 路径**并不存在**。

**诚实标注两点**(经独立检视纠正):
- issue #81 的激活条件("真实 dogfood 重放出现重复")**尚未触发**;本 ADR 是前置拉动,理由是改动小(见 D2)、且消掉一个反复出现的延期债。
- 早期草案曾用"linker 需要去重"佐证紧迫性——**撤回**:`links` 表已有唯一约束 + `ON CONFLICT`(`postgres.go:248`),linker 在自己层按 `(from_event, to_event, relation)` 幂等即可,**不依赖** ingest 去重。本 ADR 与 linker 解耦。

## 验证

代码探查(2026-06-03,main):

- `internal/ingest/handler.go:181`:`if in.ID == "" { in.ID = ulid.Make().String() }`——保留 client id,但 hook 不提供 id;`baseEvent` 返回的 map **无 `id` 键**。
- **排序与哈希链头都靠 `id`(ULID)**:`internal/store/postgres.go` `ORDER BY session_id, id ASC`(列表)、`ORDER BY id ASC LIMIT`(按 session)、`HeadHash` `ORDER BY id DESC LIMIT 1`。即**哈希链的"上一条" = 当前最大 id**;新事件入库时 prev_hash 取自该链头,故新事件 id 必须比链头**大**,链才接得对。`internal/audit/verify.go` 的链校验是**按 id-asc 顺序走 linkage**(不重算 hash),同样依赖 id 单调。
- **关键结论**:服务端单调 ULID 是**哈希链完整性载荷**。若改用产出方自生成的 ULID 当 `id`(跨进程、系统时钟有偏移),迟到事件可能拿到比链头**小**的 id → `HeadHash` 选错头 / verify linkage 断。本 ADR 因此触及 `internal/hashchain` 完整性推理,接受 / 落地走**强制 `/review`**(即便结论是"`id` 不动")。
- **已存在的 `id` 不变量违反(潜在脆弱,非 live 损坏 hook 链)**:`internal/webhooks/deploy/mapper.go:76` 把 `Idempotency-Key` 头(最长 128 字符的任意字符串)**塞进 `WireEvent.ID`**;`github/mapper.go` 把 `deliveryID` 塞进 `id`。这两者已经不是服务端 ULID。`HeadHash` 是**按 session 分**的(`WHERE session_id = $1 ORDER BY id DESC`),deploy / github 与 hook 在**不同 session_id 空间、不交叉**,故不破坏 hook 链;但**在 deploy / github 会话内**,`max(id)` 对任意字符串选头**非单调、脆弱**(一条字典序更小的 redelivery 选不出对的链头)。本 ADR 顺带把它们的 `id` 也归服务端 ULID(D1 / D2)。

## 决定

### D1. `id` 一律由**服务端**生成 ULID;不把任何产出方的 id 当 `id`

事件 `id` 保持服务端在 append 锁内赋值,从而排序与哈希链头单调性不变、verify 不受影响。这对 hook、GitHub webhook、deploy webhook **一视同仁**——后两者现在把 deliveryID / Idempotency-Key 塞进 `id` 的做法(§验证 末条)改掉,`id` 处处是服务端排序锚。

**否决备选(#81 候选 A / B)**:保留产出方 ULID 当 `id` + 409 / `ON CONFLICT DO NOTHING`。把跨进程、有时钟偏移的 ULID 塞进 `id`,就把链头单调性交给了它——一条迟到事件拿到比链头小的 id,链就断。去重不该以牺牲完整性为代价。

### D2. 每事件一个 ULID 幂等键(`idempotency_key`),由产出方生成,作唯一去重键

`id` 管排序 / 链;`idempotency_key` 管去重——解耦。键是**每事件唯一的 ULID**,不是内容派生:

- **hook**:`baseEvent` 在建事件时 `ulid.Make()` 生成一个键,放进 wire event。**每条事件一个**——`makeStopEvents` 循环里每个 thinking/text/turn_end 各自一个键。
- **GitHub webhook**:`idempotency_key = X-GitHub-Delivery`(天然每投递唯一);`id` 改服务端 ULID。
- **deploy webhook**:`idempotency_key = Idempotency-Key` 头;`id` 改服务端 ULID。

**否决备选(内容 hash `sha256(session_id, ts, kind, actor, payload)`,本 ADR 早期草案)**:**有假性去重 bug**。`baseEvent` 每次 hook 调用只盖**一个 `ts`**,而一次 Stop 在循环里发多条事件——两个脱敏后文本相同的 thinking 块、或两个相同参数的 tool_call,会算出**同一个内容 hash** → 第二条被当重复**丢掉**(丢的是真事件)。这比它要否决的 A/B 还糟(ULID 天生每事件唯一)。内容 hash 唯一的卖点"可跨路径重算一致"也是**多余的**——键反正随 wire event 持久化进 NDJSON(D4),从不重算;每事件 ULID 同样随 fallback 带回。故内容 hash = "ULID 键 + 一个碰撞 bug + 不必要的 canonical 序列化规格"。
**否决备选**:hook 加键、webhook 仍用 id 走 PK 去重——两套机制、`id` 语义不一,且不修 deploy 的 `id` 不变量违反。

### D3. 服务端按 `idempotency_key` 幂等插入,返回真实 inserted 数,批量遇重**跳过续行**

- store 加 `idempotency_key` 列 + 唯一索引;插入走 `ON CONFLICT (idempotency_key) DO NOTHING`。**memory store(`internal/store/memory.go`,测试与 `AGENT_LENS_STORE=memory` 默认后端)必须镜像同一去重**——加 `idempotency_key` 索引、命中返回 `ErrDuplicate`,否则整套测试都跑不到去重语义。
- `/v1/events` 的 `{accepted: N}` 返回**实际入库数**(非批大小);HTTP 批量遇已见键 → **跳过该条、继续**,不再 409 整批失败(现状 `writeAppendError` 把 dup 当 409 中断,使重放整批挂掉)。webhook 路径维持优雅 ack。**这是一处 HTTP 契约翻转**:重放一份已入库的文件,从今天的"409 失败"变成"200 `{accepted:0}`"——`replay.go` 现在拿非 2xx 当失败、是个隐性安全网,翻转后它会静默成功;落地须同步 `replay` 的退出码语义与相关测试。
- **静默跳重在此安全,但仅限"冻结的 wire event 被重发"**:键是每事件 ULID,两条**不同**事件几乎不可能撞同键(ULID 80 位随机),故"重复键 = 同一物理事件被重发"——丢弃总是对的(内容 hash 的"丢重可能丢真事件"风险不存在)。**边界**:ULID 键去重的是**冻结进 NDJSON 的重放**(D4);它**不**去重"transcript cursor 未推进、同一事件被重新派生"——重新派生会 `ulid.Make()` 拿到**新键**、不撞重。这是既有的 at-least-once 边界(`Send` 成功但 cursor 未 commit / 崩溃),**非本 ADR 退化**,本 ADR 也不声称解决它。每次 skip 打一条结构化日志(key/session/kind)便于排查。

**否决备选**:dup 仍 409 整批失败(候选 A)。重放本就预期撞重,整批失败让"安全重跑"无从谈起。

### D4. 键随 wire event 入 NDJSON fallback,重放原样带回 → 幂等;过渡窗诚实限定

`idempotency_key` 是 wire event 的字段,Ingest 不可达回落 sink 时一并写入;`replay` 原样 re-POST,键不变 → 服务端跳重。

**过渡窗(经检视纠正)**:本保证**仅对升级后的 hook 产生的事件**。升级前已写在 `~/.agent-lens/sessions/` 的 fallback 文件**没有键**(字段还不存在),入库时 `idempotency_key` 为 NULL、不参与去重(D5),重放它们**仍会重复**。故 **`--remove-on-success` 在整个过渡窗内保持必须**——直到确认磁盘上不再有升级前的 fallback 文件,才谈得上"对新事件可选"。一次 `replay` 可能同时处理新旧文件(混批),只看"新事件可选"会是脚枪。不试图回填旧文件(键是每事件 ULID、无法从内容重建)。

### D5. 去重轴从 `id` 搬到 `idempotency_key`;对老数据纯增量,但 webhook 的"靠什么去重"变了

`id` / 哈希链一字不动(D1)。但**去重的依据从 `id` 搬到 `idempotency_key`**,这对 webhook 路径是**机制变更、非纯增量**:今天 webhook 靠 `id = deliveryID` + 主键唯一去重;D1 把 `id` 改服务端 ULID 后,redelivery 的 `id` 不再相同,**去重必须改挂 `idempotency_key`**。因此:

- store 加 `idempotency_key` 列 + 唯一索引;**memory store 现按 `id`(`byID`)去重,必须新增独立的 `byKey` 索引**——`id` 永远唯一后 `byID` 对去重失效,redelivery 只能靠 `byKey` 撞重,否则 webhook 去重静默退化。webhook redelivery 测试是这条的回归守卫。
- 对**老数据纯增量**:老事件 `idempotency_key` 为 NULL、不去重,保持现状;唯一索引对 NULL 不约束(Postgres 多 NULL 允许;memory 镜像同语义)。无 `Idempotency-Key` 头的 webhook 投递键也是 NULL → 与今天一样不去重(本就如此,不变)。
- server 启动自迁移加列 + 索引(`AGENT_LENS_SKIP_MIGRATE` 之外)。

## Scope(本 ADR 范围)

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:`Event` 增 `idempotency_key`(string)→ `make proto`;`internal/ingest` `WireEvent` 同步。
- `cmd/agent-lens-hook`:`baseEvent` 每事件 `ulid.Make()` 生成键写入 wire event(随 transport / NDJSON fallback 一并带出)。
- `internal/store`:**postgres 与 memory 双实现**——events 加 `idempotency_key` 列/索引;memory **新增 `byKey` 索引**(`byID` 去重对 always-unique 的 `id` 失效);`AppendEvent` 走 `ON CONFLICT DO NOTHING` / memory `byKey`,命中返回 `ErrDuplicate` 并回报是否实际插入;新 Postgres migration(server 自迁移)。
- `internal/ingest/handler.go`:批量遇去重改"跳过续行 + 计数",`{accepted}` 返回真实 inserted 数;每 skip 一条结构化日志。
- `cmd/agent-lens-hook/replay.go` + `transport.go`:`replay` 遇 `{accepted:0}`(200)不再当失败(同步退出码语义);更新 `replayUsage` / `warnFallback` 里"重放会重复、务必 `--remove-on-success`"的过期措辞(改为"对升级后的事件可选,过渡窗内仍必须")。
- `internal/webhooks/github`、`internal/webhooks/deploy`:`id` 改服务端 ULID,`idempotency_key` 设 deliveryID / Idempotency-Key 头;更新各自 `*_test.go` 里"id 应等于 deliveryID"的断言(改为 `idempotency_key` 等于、`id` 是 ULID)与 handler.go:111 的过期注释。**redelivery 去重测试是 D5 机制搬移的回归守卫。**
- `internal/query` GraphQL:`Event` **透出 `idempotencyKey`**(→ `make gqlgen`)——记录某条事件的键;注意被去重**跳过的事件没有行可查**,"什么被去重了"只在 D3 的结构化日志里。

**本 ADR 不带任何代码改动。**

## 后果

- §7 `Event` 增 `idempotency_key` 字段;§10.1 增"hook 重放幂等"(`replay` 对升级后事件可安全重跑)。
- `verify` / 哈希链:**不变**——`id` 仍服务端单调 ULID,链头逻辑、`internal/hashchain` 推理原样。这正是选每事件 ULID 键(而非把键当 id)的理由。**注意**:`idempotency_key` 作为 wire event 字段,会进入服务端 `json.Marshal(in)` 的 canonical 形(像 `id` 一样被纳入事件 hash)——当前 verify 只校验 linkage、不重算 hash 故无感;但**未来落地"按内容重算 hash"的 verifier 时,必须把 `idempotency_key` 纳入 canonical 输入**,否则会与本 ADR 之后写入的事件全部 mismatch。接受/落地仍走强制 `/review`。
- D2 顺带修正 deploy / github webhook 把外部 id 塞进 `id` 的既有 `id` 不变量违反(§验证 末条)。
- 0010 / 0011 / 0012 / 0013 § 后果里"去重归 ingest 既有机制"的承诺由本 ADR 兑现。
- linker(下一步)**自己**在 `links` 唯一约束上幂等,不依赖本 ADR;本 ADR 让重放后的事件流不含重复,二者互补、不耦合。
- 数据增量:每事件一个 26 字符 ULID 键 + 一个唯一索引。比内容 hash(64 hex)更省。
- migration:加列 + 索引;老事件键 NULL、不回填(保持现状)。
- 显式留给后续:旧 fallback 文件的回填(无法从内容重建键,仅能靠 `--remove-on-success`);全局跨产出方去重策略沿用同一 `idempotency_key` 契约,不需再决策。

## 落地

**设计已锁定(Accepted),实现延后。** 接受本 ADR 把方向、键选型、id/链解耦定死,避免将来重新论证;但**不立即落码**——理由:

- #81 的激活条件("真实 dogfood 重放出现重复")**尚未触发**;"linker 需要去重"的理由已撤回(linker 自幂等)。
- 实现成本中等(proto + store×2 + 两个 webhook 迁移 + migration + `/v1/events` 409→200 契约翻转 + 强制 `/review` + 测试改),不值得为未触发的条件先付。

**落地触发条件**(任一):dogfood 实际观测到重放重复;或第二个产出方需要跨路径去重;或着手做依赖"事件至多一条"的下游(如某些 linker 聚合)。届时按 § Scope 一个 PR 落,走**强制 `/review`**(触及 `internal/hashchain` 推理与 store)。本设计已经两轮独立检视(2026-06-03)。

接受本 ADR 时,SPEC §7 把 `idempotency_key` 列为 `Event` 的(已接受、待落地)字段,与 0003–0005 EventKind "Accepted-but-unlanded" 同例;§10.1 标注"replay 幂等:设计已接受(ADR 0014),实现 gate 在 #81"。

### 实现记录(2026-06-04,#81)

落地触发为「主动前置消债」(D2/D3 早已两轮检视,且去重缺口已成 0010–0013 §后果反复引用的悬空承诺),按 § Scope 一个 PR 落,走强制 `/review`:

- **schema/codegen**:`proto/event.proto` 增 `idempotency_key = 13`(`make proto`);`schema.graphql` 增 `idempotencyKey: String`(`make gqlgen`)。
- **store**(D1/D3/D5):`store.Event.IdempotencyKey`;migration `0003_idempotency_key`(列 + **partial unique index** `WHERE idempotency_key IS NOT NULL`);postgres `AppendEvent` 走 `ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,`RowsAffected()==0`→`ErrDuplicate`,id PK 冲突仍 `ErrDuplicate`;memory 新增 `byKey` 索引去重;四个全列 SELECT + `scanEvent` 透出键。
- **ingest**(D1/D3):`WireEvent.IdempotencyKey`(`omitempty`,使无键事件 canonical 不变);`id` **无条件**服务端 ULID(移除 `if in.ID==""`);`IngestNDJSON` 遇 `ErrDuplicate` **跳过续行 + 结构化日志**,`{accepted}` 为真实入库数;`/v1/events` 由 409 翻为 200。
- **producers**(D2):hook `baseEvent` 每事件 `ulid.Make()` 填键;git post-commit 事件同样填键(也走 fallback/replay);GitHub 四个 mapper + deploy mapper 把 deliveryID / `Idempotency-Key` 头从 `id` 改挂 `idempotency_key`(顺带修 §验证 末条的 `id` 不变量违反)。
- **replay/transport**(D4):`replayUsage` / `warnFallback` 措辞改为「升级后事件可安全重跑,过渡窗内仍须 `--remove-on-success`」;`postNDJSON` 本就以 2xx 为成功,200 `{accepted:0}` 自然成功,退出码语义无需改。
- **query**(后果):`toGQLEvent` 透出 `idempotencyKey`。
- **测试**:重写 `TestIngestOverridesSubmittedID`(id 必服务端赋)/ `TestIngestDedupesOnIdempotencyKey`(200+accepted:0)/ `TestIngestMixedBatchSkipsDupKeepsRest`;webhook redelivery 断言改 `idempotency_key`(D5 回归守卫);`TestEventLinksDataLoaderBatches` 改按服务端 id 建链;新增 `TestPostgresDedupesOnIdempotencyKey`(integration,需 Docker)。
- **未验证**:postgres integration 测试因本机无 Docker 未实跑(代码 + `go vet -tags integration` 通过);`ON CONFLICT … WHERE … DO NOTHING` 的 partial-index 推理依赖 Postgres 标准语义。
- **后续仍开口**:旧 keyless fallback 文件无法回填键,过渡窗内 `--remove-on-success` 仍必须(D4)。
