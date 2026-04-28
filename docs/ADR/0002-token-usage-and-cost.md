# ADR 0002:把 token 用量纳入证据链

- 状态:Accepted
- 日期:2026-04-28
- 取代:—
- 修订:SPEC §5、§7、§10.1、§15、§17

## 背景

SPEC v0.4 §10.1 把 Claude Code hook + transcript 路径列为已知限制:"不抓 token usage 和 stop_reason;需要时切到 §10.4 的 proxy deep-mode"。§17 重复了这条限制。结果是 token 计量被推到 M4+,proxy deep-mode 成了任何 usage dashboard 的前置依赖。

新需求:可审计的证据链必须按"人 ↔ Coding Agent" 每一轮记录 token 消耗。

在动手做 M4+ proxy 之前,我们重新查了 transcript 本身。结论**推翻**了 §10.1 / §17 的那条限制。

## 验证

我们检查了 `~/.claude/projects/-Users-dongqiu-Dev-code-agent-lens/` 下两份真实 Claude Code transcript(都是这个仓库自己开发产生的 —— 同一用户、同一模型 `claude-opus-4-7`、相似工作流且 extended thinking 开着):

- 小 session(`2e5479d3-...jsonl`):**19 / 19** 条 assistant 消息都带有非空 `message.usage`。
- 大 session(`330c2b60-...jsonl`):**1080 / 1080** 条真实 assistant 消息都带 `message.usage`。另外多出一条 `<synthetic>` 消息,usage 全零、`stop_reason=stop_sequence` —— 容易识别并跳过。

`message.usage` 包含 usage 计量需要的全部维度:

| 字段 | 含义 |
|---|---|
| `input_tokens` | 非缓存的 input tokens |
| `output_tokens` | output tokens |
| `cache_read_input_tokens` | 缓存命中读取 |
| `cache_creation_input_tokens` | 写入缓存的总 token 数 |
| `cache_creation.ephemeral_5m_input_tokens` | 5 分钟 TTL 部分 |
| `cache_creation.ephemeral_1h_input_tokens` | 1 小时 TTL 部分 |
| `service_tier` | `standard` / `priority` / `batch` |
| `server_tool_use.web_search_requests` | 服务端 web-search 调用次数 |
| `server_tool_use.web_fetch_requests` | 服务端 web-fetch 调用次数 |
| `iterations` | 当 `stop_reason=tool_use` 时的子消息迭代次数 |

兄弟字段 `message.model`(例如 `claude-opus-4-7`)和 `message.stop_reason`(`end_turn` / `tool_use` / `stop_sequence` / null)就在 `usage` 旁边,同样可读。这两个也曾被 §10.1 列为"未抓",实际都在。

大 session 的聚合(1080 条消息):

- input:1,660
- output:1,174,295
- cache write:3,346,839
- **cache read:416,961,148**

cache-read 比其他维度高出**两个数量级**。任何把 cache reads 折叠进 "input" 的 usage dashboard 会把图景扭曲约 250 倍 —— **即使不算钱,这个分维也是要保留的**。

**覆盖度告警**:这两份 transcript 共享同用户、同模型、同工作流。跨项目、跨模型(haiku / sonnet / 非 Anthropic)、无 cache hit 的 transcript 还没测过。M2-E 第一次落地实现时,应当**对一份不同仓库 + 至少一份不同模型的 transcript 重新核对** `usage` schema 和 `<synthetic>` 形态,然后才能把下面的观察当作普适事实。

## 为什么不做 cost

我们考虑过一并加 cost 估算(price table × usage)。复盘后**无限期延后**:

- 厂商定价**结构**不一样,不只是数字差:Anthropic 的 cache write 按 TTL 不同有不同倍率;OpenAI 的 cached input 是平价折扣;web_search / web_fetch 这类服务端工具的按次计费在多数厂商根本没对应物。统一一个 `$` 数字会把审计员真正需要看的决策**藏起来**。
- 维护多厂商价格表是运维工作(费率变动、新档位、地区差异),会分散 Agent Lens 的核心职责 —— **记录发生了什么**。
- Token 数才是审计相关的原语。Cost 是衍生的、组织相关的关注点(billing 账户、议价费率、BYOK vs API),下游工具想算时拿原始 token 数自己算就行。

捕获路径**保持 cost-ready**:事件里存了未来算 cost 需要的所有维度(按维度分的 token、model、service_tier、vendor)。重新引入 cost 是 query / UI / config 层的添加,不需要 schema 迁移。

如果下游工具持续要求一个"原始 token 算不出来的统一 cost 视图" —— 比如审计员需要一个他们自己算不出的 `est_cost_usd` 列,或者多租户部署需要中心化治理的价格表 —— 重新评估这个决策。这里"out of scope"的措辞是**操作性**的(避免在 v1 里塞个会分散精力的 price-table 服务),不是原则性的。

## 决定

### D1. 把 usage 嵌在已有事件 payload 里,不引入新 EventKind

每条 assistant 消息派生出的事件(`text` 块 → `DECISION`,`thinking` 块 → `THOUGHT`)在 payload 里带一个可选的 `usage` 子对象。Turn 级和 session 级总额由 query 层算,不作为单独事件落库。

**否决备选**:专门一个 `EVENT_KIND_USAGE`。Usage 是关于消息的 metadata,不是独立行为。专设 kind 需要回链到产生消息的 `Link`,把事件流体积膨胀约 2×,消费者却没收益。

### D2. 标准化一个 vendor-neutral 的 `TokenUsage` shape,vendor 特定 schema 在 ingest 处归一化

```
TokenUsage {
  vendor:                  "anthropic" | "openai" | ...
  model:                   string                       // 厂商原始 model id
  service_tier?:           string
  input_tokens:            int
  output_tokens:           int
  cache_read_tokens?:      int
  cache_write_5m_tokens?:  int                          // 当前 anthropic 特有
  cache_write_1h_tokens?:  int                          // 当前 anthropic 特有
  web_search_calls?:       int
  web_fetch_calls?:        int
  raw?:                    object                       // 厂商原始块,留给取证再解析
}
```

Claude Code 映射(从 `message.usage`):

- `input_tokens`            → `input_tokens`
- `output_tokens`           → `output_tokens`
- `cache_read_input_tokens` → `cache_read_tokens`
- `cache_creation.ephemeral_5m_input_tokens` → `cache_write_5m_tokens`
- `cache_creation.ephemeral_1h_input_tokens` → `cache_write_1h_tokens`
- `server_tool_use.web_search_requests`      → `web_search_calls`
- `server_tool_use.web_fetch_requests`       → `web_fetch_calls`
- 整个 `usage` 块                              → `raw`(以便发现自己漏抓字段时反推)

可选字段的形状今天明显是 Anthropic-shaped。OpenCode / Cursor / OpenAI 厂商接入时,**扩展 shape 而非把外来概念硬塞进现有命名** —— 比如为 OpenAI 的平价折扣模式加 `cached_tokens`,仍然挂在 `TokenUsage` 这把伞下。

`raw` 是有意冗余。它每条事件多几百字节,买的是对**厂商 schema 漂移**和**我们自己归一化错误**的保险。

### D3. 没有可用 usage 的消息按 metadata-only 处理,INFO 日志记一下

`<synthetic>` 这个 model 标记(Claude Code 自己注入的 stop sequence,验证样本里观察到 1 次,usage 全零)是触发场景,但规则是更通用的:**满足任一条件**就视为 metadata-only —— `message.model == "<synthetic>"`,或 `message.usage` 缺失,或 `usage` 里所有数值字段都为零。

处理方式:正常发出非 usage 部分的事件内容、跳过 `TokenUsage` 抽取,**用 INFO 级别日志记下违规形态**以便回看时复盘是否假设破了。**不抛 error,不丢事件**。

`<synthetic>` 形态今天观察到 n=1;INFO 日志是显式 hedge,防止未来某个 Claude Code 版本开始往这类消息里填真 usage 时被我们悄悄扔掉。

## Scope(本 ADR 范围)

本 ADR 只做文档。它更新 SPEC、记录决策。具体实现落到下一个里程碑(M2-E 或 M3 子项):

- `internal/transcript/` 里加 transcript usage 抽取
- `payload.usage` shape 契约(不需要改 proto enum,payload 是 `google.protobuf.Struct`)
- GraphQL 暴露:`Event.usage`、`Turn.totalUsage`、`Session.totalUsage` resolver
- Lens UI:每轮 token 分维 + session 级总额

**本 ADR 不带任何代码改动**。

## 后果

- §10.1 / §17 那段限制语 wrong,在同一个 changeset 里被移除。Hook path 现在被声明为 usage 和 stop_reason 的真理来源;§10.4 proxy deep-mode 不再是 usage 特性的前置依赖。
- v1 证据链获得 token-usage 维度,**不需要扩 M4+ scope**。
- Cost / pricing 显式不在 scope。后续要重新引入是纯叠加(price-table 配置 + query / UI 层),不需要事件 schema 迁移。
- 跨厂商支持(OpenCode / Cursor / 自研 agent)经 D2 已经 schema-ready,但每个新厂商仍要写 mapping 函数。这部分按厂商单独跟,挂在 §10.2 / §10.3 下。
- Attestation predicate 是否包含 token 总额**故意不在这里决定**。数据已经在事件里、turn 级总额可由 query 层算出;下次修订 `agent-lens.dev/code-provenance/v1`(M3-B-2 已发)或其后续版本的人决定塞什么、扩 v1 还是 bump v2。这归给 attestation-revision 的 PR,不归本 ADR。
