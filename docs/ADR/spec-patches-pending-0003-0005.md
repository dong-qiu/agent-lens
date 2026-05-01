# SPEC.md 修订草案:配套 ADR 0003 / 0004 / 0005

- 状态:草案,待对应 ADR 全部 Accepted 时合入 SPEC.md
- 日期:2026-05-01
- 关联:ADR 0003(agent_config_snapshot)、ADR 0004(human_intervention)、ADR 0005(context_transform)

## 使用说明

本文件不修改 SPEC.md,只列出"如果 ADR 0003/0004/0005 全部 Accepted,SPEC 哪几段该怎么改"的补丁草案。每段以**当前**(直接复制 SPEC v0.5 现状)+ **修订后**(替换内容)的形式给出,便于接受时做对照合入。三个 ADR 可独立接受;若仅部分接受,合入时按对应 D 段落筛选——不要按 ADR 整体合入。

合入流程参照 ADR 0002 的先例:在同一 changeset 里 ADR 状态从"草案"改"Accepted"、SPEC.md 应用本文件对应 patch、版本号 bump 到 v0.6、版本注更新。**本文件本身在合入 PR 中删除**(不长期保留)——它是临时锚点,不是 SPEC 的一部分。

---

## §5 核心概念

### 当前

```
- TokenUsage:单条 assistant 消息的 token 计量(input / output / cache_read / cache_write 5m / cache_write 1h / web_search / web_fetch + model + service_tier)。供应商无关 schema,vendor 字段标注来源。**v1 只记录 token 数,不计算费用**——多厂商计费结构差异大且易变,留给下游消费者按需自行计算。详见 ADR 0002。
```

### 修订后(在 TokenUsage 之后追加三条)

```
- TokenUsage:单条 assistant 消息的 token 计量(...保持不变...)。详见 ADR 0002。
- AgentConfigSnapshot:agent 决策时刻所处配置的快照——指令栈(CLAUDE.md hierarchy)、settings*.json、agents / commands / skills 文件、permission mode、采集器二进制 hash。内容走 artifact store 内容寻址,事件 payload 只引 hash。SessionStart + 配置漂移触发,不每 turn 重发。详见 ADR 0003。
- HumanIntervention:人对 agent 行为的反馈事件,sub_kind 含 permission_decision / interrupt / prompt_revision / review_decision / merge_override / permission_config_change / manual_edit。与既有 tool_call / review 事件共存,通过 `target_event_id` 链回被介入的 agent 动作。详见 ADR 0004。
- ContextTransform:agent 上下文窗口在 turn 之间发生的有损变换,sub_kind 含 compaction / truncation / system_reminder_injection。`loss_hint.confidence` 区分 observed(transcript 直接读到)与 inferred(从 token-budget 启发式推断)。详见 ADR 0005。
```

来源:ADR 0003 § 决定 D1 / D2、ADR 0004 § 决定 D1、ADR 0005 § 决定 D1。

---

## §7 数据模型

### 当前

```
Event {
  ...
  kind: prompt | thought | tool_call | tool_result
       | code_change | commit | pr | test_run
       | build | deploy | review | decision
  payload: <kind-specific JSON>
  ...
}
```

```
Link {
  from_event, to_event
  relation: produces | references | reviews | builds | deploys
  confidence: 0.0..1.0
  inferred_by: rule_id | manual
}
```

### 修订后

```
Event {
  ...
  kind: prompt | thought | tool_call | tool_result
       | code_change | commit | pr | test_run
       | build | deploy | review | decision
       | agent_config_snapshot                           // ADR 0003
       | human_intervention                              // ADR 0004
       | context_transform                               // ADR 0005
  payload: <kind-specific JSON>
  ...
}
```

```
Link {
  from_event, to_event
  relation: produces | references | reviews | builds | deploys
         | intervenes                                    // ADR 0004:human_intervention → target agent event
  confidence: 0.0..1.0
  inferred_by: rule_id | manual
}
```

**不在 §7 内联三个新 payload shape 的全文**——三者均比 TokenUsage 复杂,且 schema 演进路径已写在各自 ADR 的 D 段。SPEC 只承载 enum 与一句话定义,详细 shape 引用 ADR。

`code_change` 事件预留新字段 `contributor_mix`(按 hunk 的 author 归因),首版不发射,等 IDE 插件层(M4+)。详见 ADR 0004 D4。

来源:ADR 0003 § Scope、ADR 0004 § 决定 D1 / D4 / § Scope、ADR 0005 § 决定 D1。

---

## §10.1 Claude Code(首发)

### 当前 — "已知局限" 段

```
**已知局限**:
- Claude Code transcript jsonl 不是公开稳定契约,解析按 fail-soft:未识别行跳过,不中断流。最低支持版本随发版迭代标注于 README。
- `<synthetic>` 模型标记的消息(Claude Code 自身注入的 stop-sequence 占位,usage 全 0)按已知形态跳过 usage 提取,不报错、不丢消息体。
- **思考内容(thinking 文本)**:Claude Code 写 transcript 时只保留 `signature` 字段,**原文不持久化**。§10.1 拿不到原文。每条 assistant 消息中被丢弃的 thinking 块**数量**显式记录在派生 DECISION 事件的 `payload.thinking_redacted_by_claude_code`,避免审计报告把"被吞"误读为"无思考"。要拿原文得走 §10.4。
- 仍**没有**的能力:实时拦截 / token 流式即时反馈 / policy gate。要这些得走 §10.4。
```

### 修订后

在"思考内容"条目之后、"仍没有的能力"条目之前**插入**:

```
- **工具空间与采样参数**:本轮可用工具集合(active MCP server、tool catalog)、permission mode 即时值、thinking budget、temperature 等 model_params 在 hook 路径不可得。`agent_config_snapshot` 事件中这两块字段标 `null` 并在 metadata 列入 `unknown_fields`,审计端能区分"工具空间为空" vs "采集路径看不到"。要拿到这些得走 §10.4。详见 ADR 0003 D4。
- **Compaction 边界**:harness 自动 compaction 在 transcript 里是否显式标记**未经系统验证**,首版按 token-budget 启发式推断,生成 `context_transform.compaction` 事件并置 `loss_hint.confidence = "inferred"`。验证通过则 confidence 升到 `observed`;Truncation 与 system reminder 注入在 transcript 中可见,直接观测。详见 ADR 0005 D5。
- **手工编辑归因**:agent 改完代码 → 人手在 IDE 微调 → commit 这条混合贡献路径,在 §10.1 路径不可还原。`HumanIntervention.manual_edit` 与 `code_change.contributor_mix` 字段位预留,等 IDE 插件层(M4+)。详见 ADR 0004 D4。
```

"仍没有的能力" 条目保持原状不变。

### 当前 — "事件捕获路径" 段

包含 `SessionStart` / `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / `Stop` 列表与 transcript 旁路说明。

### 修订后 — 在事件捕获路径列表末尾追加

```
- **配置快照**(SessionStart + 配置漂移):每次 SessionStart 起一条 `agent_config_snapshot` 事件,bundle 内容走 artifact store。`UserPromptSubmit` / `PreToolUse` 时若指令文件 / settings hash 漂移,补发新快照。`hook_binary_sha256` 在 SessionStart 算一次,与 bundle 同事件入 hash chain,作为 capture-time attestation。详见 ADR 0003。
- **人工干预**(PreToolUse 派生 + Stop 启发式 + GitHub webhook 派生):`PreToolUse` permission decision 派生 `human_intervention.permission_decision`(与 tool_call 共存,via `target_event_id` 链回);`Stop` 时按 `stop_reason == null` 且无后续 tool_result 启发式推断 `interrupt`;GitHub `pull_request_review` handler 派生 `review_decision`,merge 事件回扫 request_changes 状态派生 `merge_override`。详见 ADR 0004 D2 / D3 / D5。
- **上下文变换**(transcript 解析 + PostToolUse 截断检测):transcript 解析新增识别 compaction(降级为启发式)、system reminder 注入(以 hash 去重发首次);PostToolUse 检测 tool_result 截断占位符并发 `context_transform.truncation`。详见 ADR 0005 D3 / D4 / D5。
```

来源:ADR 0003 § 决定 D3 / D4 / D5、ADR 0004 § 决定 D2 / D3 / D4 / D5、ADR 0005 § 决定 D3 / D4 / D5。

---

## §15 风险与开放问题

### 当前 — 表格末尾

```
| R7 | 跨厂商 TokenUsage 可比性:OpenCode / Cursor / 自研 Agent 的 usage schema 不一致——尤其 cache 语义(...). 详见 ADR 0002 D2。 |
```

### 修订后 — 表格追加一行

```
| R8 | Compaction 与部分 context 变换在 §10.1 hook 路径仅能启发式探测,精确建模等 §10.4 代理深模式或 IDE 插件层。事件载体(`context_transform`)已就位,`loss_hint.confidence = inferred / observed` 标记区分,审计端不会被静默漏报。详见 ADR 0005 D5。 |
```

来源:ADR 0005 § 后果。

(**无新风险来自 ADR 0003 / 0004**——0003 关闭的是采集器真伪空白,0004 关闭的是人工干预空白,二者都是补缺而非引入风险。)

---

## §17 自观测(Dogfooding)

### 当前 — "已具备的捕获深度" 段

```
**已具备的捕获深度**(截至 v0.5):§10.1 的 hook 直采 + transcript 旁路使得激活后能拿到 prompt / 工具调用 / 工具结果 / thinking(启用 extended thinking 时)/ assistant 文本回复 / turn 边界 / **每条 assistant 消息的 token 用量与 stop_reason**(含 cache 5m/1h 分桶、server_tool_use 计数、service_tier、model)。仍要等 §10.4 代理深模式才能拿到的能力:实时拦截、流式 token 即时反馈、policy gate。
```

### 修订后

```
**已具备的捕获深度**(截至 v0.6):§10.1 的 hook 直采 + transcript 旁路使得激活后能拿到 prompt / 工具调用 / 工具结果 / thinking(启用 extended thinking 时)/ assistant 文本回复 / turn 边界 / **每条 assistant 消息的 token 用量与 stop_reason**(含 cache 5m/1h 分桶、server_tool_use 计数、service_tier、model)。

经 ADR 0003 / 0004 / 0005 接受后,本路径还能捕获:

- **决策时配置快照**:指令栈合并 hash、settings 与 hook 注册、采集器二进制 hash(capture-time attestation),按 SessionStart + 配置漂移触发(ADR 0003)。dogfood 期 SPEC.md / CLAUDE.md / hook 高频修改时,timeline 上能看到每次配置漂移点。
- **人工干预事件**:permission 决策(allow / allow_always / deny / edit)、ESC 打断(启发式)、PR review_decision、merge_override、permissions 黑白名单变更(ADR 0004)。
- **上下文变换**:compaction(启发式 inferred)、tool_result truncation、system reminder 注入(ADR 0005)。

仍要等 §10.4 代理深模式才能拿到的能力:实时拦截、流式 token 即时反馈、policy gate、精确 compaction 边界、本轮工具空间快照、模型采样参数。
```

### "激活时要做的事" 段补一条

在现有清单末尾追加:

```
- **配置 bundle 准备**(ADR 0003 配套):本仓库的 `~/.claude/settings.json` / `<repo>/.claude/settings.local.json` 在 dogfood 激活前过一次脱敏审查——D6 redaction 是兜底,但人工预审能避开"开发期临时塞了 token"这种事故。
```

来源:ADR 0003 § 后果、ADR 0004 § 后果、ADR 0005 § 后果。

---

## 版本注与 SPEC 头

### 当前(SPEC.md 第 1–6 行)

```
# Agent Lens — 项目 SPEC

> 版本:v0.5(2026-04-28)
> 状态:草案 / 规划阶段
>
> v0.5 变更:把每轮交互的 token 用量纳入证据链。撤回 v0.4 §10.1/§17 关于"hook 路径不抓 token / stop_reason"的限制(已被 transcript 验证证伪)。**价格 / 费用估算明确排除在 v1 范围之外**——多厂商计费结构差异大,token 数才是审计相关原语。详见 `docs/ADR/0002-token-usage-and-cost.md`。
```

### 修订后

```
# Agent Lens — 项目 SPEC

> 版本:v0.6(2026-MM-DD,合入时填实际日期)
> 状态:草案 / 规划阶段
>
> v0.6 变更:把"agent 决策时所处配置"、"人对 agent 行为的反馈"、"context 在 turn 间的有损变换"分别升为头等事件,新增 EventKind `agent_config_snapshot` / `human_intervention` / `context_transform`。新增 Link.relation `intervenes`。新增 R8(compaction 启发式建模)。capture-time attestation 内联进配置快照,与事件 hash chain 同节点定锚。详见 `docs/ADR/0003-agent-config-snapshot.md` / `0004-human-intervention-events.md` / `0005-context-transform-events.md`。
>
> v0.5 变更:(...保留原文...)。
```

如三个 ADR 不全部同时 Accepted,版本注按实际接受范围裁剪;但 minor bump 到 v0.6 即可,不需要按 ADR 数量分别 bump。

---

## 合入清单

合入 PR 检查项(对应每个 ADR 单独勾选):

**ADR 0003 接受时**:
- [ ] §5 加 AgentConfigSnapshot 条目
- [ ] §7 EventKind 加 `agent_config_snapshot`
- [ ] §10.1 已知局限加"工具空间与采样参数"条目
- [ ] §10.1 事件捕获路径加"配置快照"条目
- [ ] §17 已具备捕获深度段更新
- [ ] §17 激活时要做的事加 bundle 脱敏审查条目
- [ ] ADR 0003 状态改 Accepted
- [ ] 版本注更新

**ADR 0004 接受时**:
- [ ] §5 加 HumanIntervention 条目
- [ ] §7 EventKind 加 `human_intervention`、Link.relation 加 `intervenes`、`code_change.contributor_mix` 字段位预留说明
- [ ] §10.1 已知局限加"手工编辑归因"条目
- [ ] §10.1 事件捕获路径加"人工干预"条目
- [ ] §17 已具备捕获深度段更新
- [ ] ADR 0004 状态改 Accepted
- [ ] 版本注更新

**ADR 0005 接受时**:
- [ ] §5 加 ContextTransform 条目
- [ ] §7 EventKind 加 `context_transform`
- [ ] §10.1 已知局限加"Compaction 边界"条目
- [ ] §10.1 事件捕获路径加"上下文变换"条目
- [ ] §15 加 R8
- [ ] §17 已具备捕获深度段更新
- [ ] ADR 0005 状态改 Accepted
- [ ] 版本注更新

合入时本文件(`spec-patches-pending-0003-0005.md`)应在同一 commit 中删除——它的存在意义是让接受流程可对照,长期保留会与 SPEC 实际状态产生漂移。
