# ADR 0005:把 context 变换升为头等事件

- 状态:草案
- 日期:2026-05-01
- 取代:—
- 修订(待 Accepted):SPEC §5、§7、§10.1、§15、§17

## 背景

agent 实际"看到"的上下文与 Agent Lens 当前事件流之间存在系统性差距:

1. **Compaction**:harness 在 context 接近窗口时把多条历史消息压成一条 summary,原文从 in-conversation 视角丢失。Claude Code 自动 compact 在长 session 经常发生(ADR 0002 的样本里,本仓库 1080 条 assistant 消息的 transcript 已经多次发生过)。
2. **Truncation**:tool result / 大文件读取 / web fetch 结果过长被 harness 截断,agent 看到的是 `[truncated, N bytes elided]` 之类占位。截断点决定 agent 是否能看到关键证据。
3. **System reminder 注入**:harness 自动塞进上下文的提醒(auto-memory 头部、当前 todo 状态、tool 列表、安全提示)——直接影响 agent 行为,但既不在用户 prompt 也不在 agent 输出里。
4. **Compaction summary 本身**:summary 文本是有损变换的产物。其中漏掉/扭曲了的事实,下游决策就建在错误前提上。

当前 schema 完全不表达这些。审计员看到 turn N 的 agent 行为"忽然变了",无法判断是 prompt 变了、还是 context 被悄悄重写了。这正是"agent 为什么这次没看到 X"这类反事实问题的根。

§15 R1(thinking 落盘隐私)讨论 thinking 文本要不要存;本 ADR 讨论**上下文本身的所有有损变换**要不要可见——更广,且互不冲突。

## 验证

Claude Code transcript 对这四类的可观测性**未经系统验证**,先列已知形态作为下界,落地按 ADR 0002 同款做法做覆盖度校验:

| 变换类别 | transcript 可见性(未验证) | 推断信号 |
|---|---|---|
| Compaction | **疑似可见** | transcript 中可能出现 system / tool_result 形态的"compacted" 标记,待实际样本核对 |
| Truncation(tool result) | **可见** | tool_result 块内带显式截断占位字符串(如 `[response truncated]`);harness 版本相关 |
| System reminder 注入 | **可见** | transcript 里 user message 内嵌 `<system-reminder>` tag |
| Compaction summary 文本 | **疑似可见**(取决于 1) | 若 compaction 可见,summary 多半作为某条 user/system message 的内容存在 |

落地实现(M3 子项 / M4 起步)开工前,**必须**用至少一份发生过 compaction 的真实 transcript 验证以上四类的实际形态,并在实施 PR 中记录假设与缺口(参 ADR 0002 的"覆盖度告警"段落)。如果 compaction 事件在 transcript 不可见,本 ADR 的 D5 给出降级方案。

## 决定

### D1. 新增 EventKind `context_transform`,按 sub_kind 区分

```
ContextTransform {
  sub_kind:        "compaction" | "truncation" | "system_reminder_injection"
  triggered_by:    "harness_auto" | "user_explicit" | "tool_limit"
  applied_at:      timestamp
  before: {
    message_ids?:    [event_id]                  // 被压缩 / 截断的原事件
    bytes?:          int
    content_sha256?: string                      // 原文的 hash,即便原文已落对应 event 中
  }
  after: {
    summary_text?:    string                     // 仅 compaction(短的可 inline,见 D2)
    summary_sha256?:  string                     // 长的走 artifact store
    bytes?:           int
    placeholder?:     string                     // truncation 用的占位字符串
    injected_text?:   string                     // system_reminder_injection 用
  }
  loss_hint?: {
    description:    string                       // "tool_result truncated to 4096 bytes" 等
    confidence:     "observed" | "inferred"      // 见 D5
  }
}
```

`before.message_ids` 让 trace 视图把变换前的原事件定位到——即便其内容已从 agent 当前视角消失,事件存储仍有它(append-only)。`loss_hint.confidence` 显式区分"我看到 transcript 标记了这次 compaction" vs "我从 token-budget 推断出有过 compaction",见 D5。

### D2. summary 文本走 artifact store + 内容寻址,事件 payload 只引 hash(短文本可 inline)

Compaction summary 可能很长,且大概率重复(同一种 turn 集合压出相似 summary),inline 进 payload 浪费空间。沿用 ADR 0003 同款策略:实际文本进 §7 artifact store,payload 只载 `summary_sha256`。

阈值:`bytes < 1 KB` 时 inline 到 `after.summary_text`(免一次 artifact 解引用);否则只填 `summary_sha256`。inline 与 artifact 互斥,不重复存。

**否决备选**:summary 全部直进 payload。短期方便、长期撑大 events 表;artifact store 已经在跑,复用零成本。

### D3. system reminder 注入分静态 / 动态两种处理

- **静态 reminder**(auto-memory 头部、固定提示):Claude Code 在每个 user turn 都注入相同 / 近似内容。每次都发事件会噪音爆炸。规则:**以注入文本的 content hash 为 key,同一 session 内同 hash 只发首次**;后续仅在 `agent_config_snapshot`(ADR 0003)的 metadata 里累计 `reminder_hashes_seen` 去重。
- **动态 reminder**(`<system-reminder>` 块内容因当下状态变化而变,如 todo 列表变更):每次都发,因为 hash 每次都变。

判定静 / 动态的方法:就是哈希。规则的 phrasing 偏简单,故意——任何更复杂的"语义聚类"都会是新故障源。

**否决备选**:全发。每个 user turn 都拖一条 reminder 事件,事件量 +N×turn,审计端要从噪音里筛信号。基于 hash 去重已经足够稳。

### D4. truncation 由 PostToolUse hook 直接产出

Claude Code 在 tool_result 落 transcript 前就截断,hook 看到的是已截断版本。所以 PostToolUse handler 必须:

- 比较实际工具产出的字节数 vs transcript 中 tool_result 块字节数(前者由 hook 自行执行工具时观察、或由 harness 透传——此处依赖 hook 接口实际能力,**待落地核对**);
- 不一致即发 `context_transform.truncation` 事件;
- 原文优先级:能拿到原文则进 artifact store(`before.content_sha256` + 原文 blob),拿不到则只记字节数差。

**已知风险**:Claude Code 默认 hook 不重跑工具,也不直接给"未截断版本"。这条 D4 的实际可达性取决于 hook payload 是否携带原始 tool 执行 stdout/stderr 长度元数据——未验证。验证未通过则 D4 降级:仅在 tool_result 文本中检测到截断占位符(如 `[response truncated]`、`...truncated...` 等已知模式)时发事件,不试图保留原文。降级模式下 `loss_hint.confidence = "inferred"`。

### D5. compaction 不可观测时的降级方案

如果 §验证 段的实测结论是 transcript **不显式标记 compaction**,本 ADR 退到三档备选,按优先级:

1. **token-budget 启发式**:transcript 中 assistant 消息累计 input_tokens 出现"骤降而 turn 数未减"——压缩痕迹。生成 `context_transform.compaction` 事件,置 `loss_hint.confidence = "inferred"`、`loss_hint.description = "inferred from input_tokens decrease at turn N"`。误差大但不为零,且对审计端**显式声明置信度**。
2. **等 §10.4 代理深模式**:proxy 看请求体,能精准识别 harness 发了什么 / 替换了什么。这是最终方案,但 v1 不上线。
3. **接受缺失**:在 §15 增一条新风险 R8("compaction 不可见"),等长期方案。

发布顺序:落地时先实现降级 1(成本极低,只需读 ADR 0002 已经在抓的 input_tokens 序列);§10.4 上线后切方案 2;不进 §3 备选——审计端宁可看到带 `confidence: inferred` 标记的 noisy 事件,也不该看到一个**已知有变换却不告诉你**的 timeline。

### D6. compaction summary 文本的 redaction 与 thinking 同等

§12 默认 redaction 在 hook 出口处生效。compaction summary 可能含密钥 / PII(被压缩的原文里如有,summary 里就有——summary 是有损变换但**不是**净化变换)。落 artifact store 之前必须过 redaction,与 thinking 文本同档。

`raw` 形式不保留——summary 已经是有损产物,再保留"未脱敏 raw"会反向放大泄露面。这与 ADR 0002 在 token usage 上保留 `raw` 的策略相反,是因为两边的隐私 / 取证权衡不同:usage 是数字(无 PII 风险,raw 提供 schema 漂移保险);summary 是文本(有 PII 风险,raw 没有同等收益)。

### D7. truncation 与 compaction 的 hash chain 处理

`context_transform` 事件本身正常进 hash chain。但要注意:`before.message_ids` 引用的是**已经入库**的事件,它们在 hash chain 中位于本事件之前。这是正常的因果方向,不破坏 append-only 与哈希链不变量。审计回放时,如果 transform 事件指向的 `message_ids` 在 chain 中**不存在**(或 hash 不匹配),verify 报错——这一致性检查与 §11 attestation 的边界检查同构,实施时复用 `internal/hashchain/verify.go`。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:增加 `EVENT_KIND_CONTEXT_TRANSFORM`。
- `internal/transcript/`:扩 transcript 解析,识别 compaction / truncation / system reminder 三种形态。落地前必须先做 §验证 段的 transcript 实测核对。
- `internal/redact/`:summary 脱敏挂在 hook 出口管线(与 ADR 0003 共用 redaction 出口)。
- `internal/hashchain/verify.go`:增加 `before.message_ids` 引用一致性检查。
- GraphQL + Lens UI:timeline 在两条 turn 之间插入 transform 节点;展开看 before / after,长 summary 可走已落地的 Monaco viewer(PR #44/#46)。

**本 ADR 不带任何代码改动**。

## 后果

- §10.1 "已知局限" 缩小一格:之前"compaction / truncation 不可见"是隐含限制,现在变成"已建模、首版按降级方案处理"。
- §15 加 R8:"compaction 在 §10.1 路径仅能启发式探测,精确建模等 §10.4"。R1(thinking 落盘)独立,不涉及。
- §17 dogfood 受益直接:本仓库长 session 触发 compaction 频繁,引入此 ADR 后能看到压缩点;system reminder 在 dogfood 调试 hook 行为时尤其有用——能看出"这条 reminder 在 turn N 第一次出现"。
- attestation predicate(§11)暂不收纳 transform 事件——`code-provenance/v1` 关注 commit ↔ turn ↔ prompt 链路,transform 是 turn 之间的内部事件,不在阶段边界。
- 与 ADR 0003 / 0004 的关系:三者新增 EventKind 互不冲突,linker 把它们拼回到对应 turn / agent 行为节点上。0003 是 session/turn 级配置锚点(漂移点),0004 是 turn 内人工动作,0005 是 turn 间 context 变换。三者合起来形成"agent 决策时身处什么状态"的完整上下文模型。
- 事件量预估:每 session 1–10 条 compaction(降级模式可能更多,因启发式假阳)、5–50 条 truncation(高 tool 输出 session)、首次出现的 reminder 数量(通常 < 10)。聚合后量级与 ADR 0004 的 human_intervention 相仿,远低于 tool_call。
- D1 中字段 `loss_hint.confidence` 的设计是面向**所有未来 v1 之外的扩展捕获方式**的——任何新 capture 路径(IDE 插件、§10.4 proxy)接入时,只需把 confidence 升到 `observed` 即可,审计端不需要因为捕获方式升级而改 query。
