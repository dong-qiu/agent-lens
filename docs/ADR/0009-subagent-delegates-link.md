# ADR 0009:Sub-agent 父→子自动链接(`delegates`,基于 SubagentStart 桥接)

- 状态:草案
- 日期:2026-05-27
- 取代:在 v0.2 范围内重新激活 ADR 0007 D4(`delegates` 关系),取代 ADR 0008 D3(v0.1 撤回 `delegates`)
- 修订(待 Accepted):SPEC §7(`Relation` 枚举加 `delegates`)、§10.1(sub-agent 派发段)

## 背景

ADR 0008(v0.1)**撤回**了 ADR 0007 D4 承诺的 `delegates` link:当时父侧 Agent `tool_result.response.agentId`(17 字符 hex)与子 sub-agent 的 hook `session_id`(UUID)之间**没有任何 surface 出来的映射**,A/B/C/D 候选要么需上游 Claude Code 改、要么是脆策略(文件系统 marker / 时间相关启发)。ADR 0008 明确"并发场景错连边比不连边更坏",在 v0.1 不引入。

**情况变了。** ADR 0008 实证(2026-05-09)之后,Claude Code 新增了 **`SubagentStart` / `SubagentStop`** hook:`SubagentStart` 在**子 agent 自己的 session 上下文**触发,payload 同时携带 `agent_id` 与子 `session_id`。issue #85 PR1(#112,已合并)已把这两个 hook 注册并以 `decision` marker 捕获,payload 带 `agent_id` / `agent_type`。

这把 ADR 0008 否决的脆路径替换成一条**确定性 id 匹配**路径:父侧 tool_result 带 `agentId`,子侧 SubagentStart 带 `agent_id` + `session_id`。若两个 id 相等,父→子映射唯一确定,**不依赖时间窗、不怕并发派发**——恰好绕开了 ADR 0008 否决 C 的核心理由。本 ADR 决定在 v0.2 据此落 `delegates` link。

## 验证

分三层,**诚实区分已确认与待测**(沿用 ADR 0004 §验证"显式标注未实测假设"的做法):

| 层 | 状态 | 依据 |
|---|---|---|
| 父侧 `tool_result.response.agentId` 可捕获 | **已确认** | dogfood sink 多条 Agent TOOL_RESULT 均含 `response.agentId`,16 字符 hex(实例 `a06a387fa5403439d`) |
| 子侧 `SubagentStart` 捕获(带 `agent_id` + `session_id`) | **代码已 ship(#112),hook 形态待实测** | hook 存在性与字段名(`agent_id` / `agent_type`)来自当前 Claude Code 文档(经 claude-code-guide 查证);捕获 fail-soft,但"事件确实触发且 payload 如此"尚未 dogfood 实测 |
| **`parent.tool_result.response.agentId == child.SubagentStart.agent_id`** | **未测——本 ADR 的枢纽假设** | 见下 |

### 待测枢纽:两个 id 是否相等

这是整条桥接成立与否的关键,**Accept 本 ADR 前必须实测**。方法:

1. `agent-lens-hook setup --personal` 装上 #112 的 hook(含 SubagentStart/Stop)。
2. 跑一个会派发 sub-agent(Task 工具)的真实 session。
3. 从 sink 取父侧 Agent `tool_result` 的 `response.agentId`,与子 session 的 `subagent_start` 事件 `payload.agent_id` 比对,记录:
   - 是否相等(注意格式:子侧文档示例带 `agent-` 前缀,父侧为裸 hex——可能需归一,见 D5);
   - 并发派发多个 sub-agent 时,每对是否仍唯一可配。

结论落到本 ADR §验证(实测后补),确认成立才把状态推进 Accepted 并落代码。**若证伪见 D7 的 fallback。**

## 决定

### D1. `proto/link.proto` 的 `Relation` 枚举增加 `RELATION_DELEGATES`

重新激活 ADR 0007 D4 的关系词。值追加在枚举末尾(`= 6`),不复用既有编号(proto 向后兼容)。`make proto` 重生绑定。

**否决备选**:复用 `RELATION_REFERENCES` 表达派发。语义不同——`references` 是工件引用,`delegates` 是"父把一段工作委派给子 agent",审计上要能直接区分"哪些工作是被委派出去的"。

### D2. 桥接键是 `agent_id`,linker 维护 `agent_id → child session` 映射

linker 消费子侧 `subagent_start` 事件(`payload.agent_id`,event.session_id = 子 session),建 `agent_id → 子 session` 索引;再用父侧 Agent `tool_result` 的 `response.agentId` 查该索引命中子 session。

**否决备选**:① 时间相关(ADR 0008 C)——并发即错,已否。② 文件系统 marker(ADR 0008 B)——脆、引入 FS 约定后收回成本高,已否。③ `outputFile` 路径反查——脆,仅作 D7 fallback。

### D3. 发 `Link{from = 父 Agent tool_result 事件, to = 子 SubagentStart 事件, relation = delegates}`

`Link` 连两个 event。`from` 取**携带 `agentId` 的父侧 tool_result 事件**(匹配键所在);`to` 取**子 session 的 `subagent_start` 事件**(子 session 入口 + 携 `agent_id`,天然锚点)。trace / graph 视图据此从父派发点跳到子 session。

**否决备选**:`from` 取父 PreToolUse(Agent) tool_call(语义上"派发意图"更贴)。但 `agentId` 只在 tool_result(PostToolUse)里,tool_call 无匹配键;若坚持从 tool_call 出,linker 还要先把 tool_call↔tool_result 配对,徒增一跳。v0.2 先从 tool_result 出,够用;未来要"派发意图"语义可再加 tool_call→tool_result 内部边。

### D4. 确定性匹配,这是相对 ADR 0008 否决项的核心改进

`agent_id` 在一次 session 内唯一标识一个 sub-agent 派发,匹配不依赖时间窗。**并发派发多个 sub-agent 不再出错**——这正是 ADR 0008 否决时间启发(C)的核心顾虑,本路径从根上消除。§17 dogfood 高频并行 worktree / sub-agent 场景因此可用。

### D5. `agent_id` 格式归一

实测若发现子侧 `agent_id` 带前缀(文档示例 `agent-abc123`)而父侧 `agentId` 为裸 hex(`a06a387fa5403439d`),linker 在比对前做归一(strip 已知前缀 / 取公共子串)。具体规则**待 §验证 实测两侧真实取值后定**,写入落地 PR 的 linker 规则注释。

### D6. `confidence` 与 `inferred_by`

确定性 id 匹配 → `confidence = 1.0`,`inferred_by = "subagent-agentid-match"`(规则 id)。区别于既有时间/ref 类启发关系的较低 confidence,审计端能看出这条边是强匹配。

### D7. 若 §验证 证伪(两 id 不等)

不强行连边(守 ADR 0008"错连边比不连边坏"的原则)。fallback 顺位:
1. 退回 ADR 0008 现状(父侧元数据 + 子独立 session + 人眼对应),本 ADR 仅保留"已加 RELATION_DELEGATES 词位"备未来用;
2. 评估 `outputFile` 路径反查(D2 否决③)作为脆 best-effort,但需显式低 `confidence`。

证伪不浪费 PR1:SubagentStart/Stop 生命周期捕获本身已是 transparency 增量(timeline 上能看到 sub-agent 起止),独立于 link 是否成立。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/link.proto`:加 `RELATION_DELEGATES`(D1)。`make proto` 重生。
- `internal/linking/`:`agent_id → 子 session` 索引 + 父侧 `agentId` 匹配 + emit `delegates` link(D2 / D3 / D6)。
- GraphQL + Lens UI:causal graph 增 `delegates` 边的可视区分(沿 PR #42 既有三类边的样式体系)。
- §验证 的实测脚本(setup --personal + 派发 + 比对),作为落地 PR 的前置 gate。

**本 ADR 不带任何代码改动。**

## 后果

- SPEC §7 `Relation` 枚举长一个值;§10.1 sub-agent 段把"`delegates` link 留 v0.2"更新为"基于 SubagentStart.agent_id 桥接落地"(实测确认后)。
- ADR 0007 D4 的 `delegates` 承诺在 v0.2 **重新激活**;ADR 0008 D3(v0.1 撤回)被本 ADR 在 v0.2 范围取代。两 ADR 仍 append-only 保留,记录"v0.1 撤回 → v0.2 因 SubagentStart 出现而恢复"的完整决策轨迹。
- 数据增量:每次 sub-agent 派发新增 1 条 `delegates` link,与派发频次同阶,低。
- linking 准确度(§15 R2)受益:`delegates` 是确定性强归因边,优于既有时间/ref 启发。
- 与 ADR 0004 的关系:`human_intervention` 与 `delegates` 正交;若未来人发起的 skill / sub-agent 派发要标"人发起",由 ADR 0004 路径承载,不与本 ADR 的 agent→agent 委派冲突。
- **风险**:整条设计押在 §验证 的枢纽等式上。本 ADR 以草案推进、实测确认前不 Accept、不写 schema/linker 代码——把风险关在草案窗口内(沿 ADR 0004 先例)。
