# ADR 0015:把 skill 注入指令正文升为 `context_transform.skill_instruction_injection`

- 状态:草案
- 日期:2026-06-05
- 取代:—
- 修订(待 Accepted):SPEC §5(events schema:`context_transform.sub_kind` 集合增 `skill_instruction_injection`)、§10.1(Claude Code 捕获:新增 transcript 侧 skill 正文抽取与 `tool_use_id` 关联)。均为既有结构上的增量,**不新增 EventKind**,落地 PR 同 PR 改 SPEC 即可,无需 SPEC patch 文件。

## 背景

#101 把一次 skill / slash-command 调用拆成两个缺口:

- **gap 1(已落地,#111)**:`Skill` 工具的 `tool_call` 事件加 `payload.skill = {name, args}` 判别符,"跑了哪个 skill"可查询。免 ADR、已带测试。
- **gap 2(本 ADR / #110)**:skill 展开后**注入给 agent 的指令正文**——即 skill 实际"让 agent 去做什么"——目前任何路径都采集不到。

gap 2 重要,因为该正文是一段**二阶 prompt**:它和用户原始 prompt 一样驱动了 agent 后续行为,但既不在用户输入里、也不在 agent 输出里。审计员看到 agent 在某个 turn"忽然按某套流程办事",却无法看到那套流程的指令来源。这与 ADR 0005 列的 system reminder 注入是同一类"既不在 prompt 也不在输出、却左右行为"的上下文盲区。

两条采集路径今天都漏掉它:

1. **Hook 路径**:`PostToolUse` 的 `tool_response` 只有 `{success, commandName}`(`cmd/agent-lens-hook/claude.go makeToolResult`)。正文不在其中。
2. **Transcript 路径**:`internal/transcript/reader.go parseLine` 对 `user` 条目只抽 `<system-reminder>`(`parseUserReminders`,ADR 0005 D3),对 `assistant` 只抽 `thinking`/`text`,且完全不抽 `tool_use` 块。skill 正文以普通 `user` 条目到达,直接被丢弃。

## 验证

针对本仓库一次真实 `/self-review` 调用,核对 Claude Code transcript(`…/feat-v4/dc0261a2….jsonl`),确证正文形态、挂链键与体量:

| 维度 | 实证结论 |
|---|---|
| 正文载体 | 紧随 Skill `tool_use` 的 `user`-role 条目,`isMeta:true`、`userType:external`,内容为单个普通 `text` 块,**不**带 `<system-reminder>`/`<command-*>` 包裹 |
| 挂链键 | 正文条目带 entry 级 `sourceToolUseID = toolu_013AB…`,**精确等于** Skill `tool_use.id`。天然 1:1 link key |
| 体量 | 该次正文 8399 B;含运行时前缀(`Base directory for this skill: …`) |
| 与静态 SKILL.md 关系 | 近似但**非字节相同**(正文 text 8361 B vs 静态 SKILL.md 8994 B):有运行时 header,且 args 可被插值(本次 self-review 无 args,/review 100 这类有)。**不是** ADR 0003 静态快照的纯重复 |
| Hook 可见性 | 同一调用的 `tool_result` 条目内容仅 `"Launching skill: self-review"`,印证 hook 路径结构性够不到正文 |

附:`claudeHookInput` 目前**未**解析 `tool_use_id`(Claude Code hook 输入里有,结构体没接)。因此 gap 1 的 `tool_call` 现在不携带 `toolu_…` id——挂链需 hook 侧补采(见 D3)。

> 验证按 ADR 0002 同款"先列已知形态作下界"的要求做;`isMeta`/`sourceToolUseID` 属 transcript schema 字段,非公开稳定契约,落地的 reader 必须 fail-soft(缺字段即跳过,绝不报错)。

## 决定

### D1. 复用 `context_transform`,新增 `sub_kind = skill_instruction_injection`,**不新增 EventKind**

skill 正文本质是"调用时被注入 agent 上下文的指令文本",与 ADR 0005 既有的 `system_reminder_injection` 是同胞:都是 harness 把一段非用户、非 agent 的文本塞进上下文并影响行为。沿用既有判别机制——payload 带 `sub_kind: "skill_instruction_injection"`、`loss_hint.confidence`——与 `compaction`(ADR 0013)、`system_reminder_injection`(ADR 0005 D3)同构。

**否决备选**:

- **新建 EventKind `skill_instruction`**:与 `context_transform` 语义重叠,且 CLAUDE.md 把"新 EventKind"列为需重量级论证(proto / `validKinds` / GraphQL enum / 文档全套)。收益(独立查询)用 sub_kind 判别符同样能拿到,不值这片表面。
- **塞进 Skill `tool_call` 的 payload**:~8KB 正文会让每个 tool_call 膨胀,且正文与"调用发生"这一小而可查询的标记(gap 1)混为一谈,无法独立脱敏 / 限流 / 挂链。issue 本身把脱敏+体量列为顾虑,正是要把正文拆成独立事件的理由。

### D2. 采集走 transcript 路径:扩 `reader.go` 抽 `isMeta + sourceToolUseID` 的 user 正文

Hook 路径结构性够不到正文(验证已证),只能走 transcript。`parseLine` 增一条:`user` 条目若带 `sourceToolUseID` 且 `isMeta:true`,抽其 `text` 块作 skill 正文,产出 `context_transform.skill_instruction_injection` 事件,actor 记 `{type:system, id:claude-code}`(与 system_reminder_injection 同)。

fail-soft 红线:`sourceToolUseID`/`isMeta` 缺失即视为普通 user 条目跳过;正文为空跳过。绝不因 transcript schema 漂移而报错或 block。

### D3. 挂链:hook 补采 `tool_use_id` 挂在 Skill `tool_call` 上,正文事件以 `sourceToolUseID` 作 ref,linker 按键对接

- `claudeHookInput` 增 `ToolUseID string json:"tool_use_id"`;`makeToolCall` 把它作为 `tool_call` 事件的 ref(或 payload 字段)落库。
- 正文事件携带 `sourceToolUseID` 作 ref。
- linker 按 `tool_use_id == sourceToolUseID` 建 `Link`(关系名落地时定,候选 `injects` 或复用 `intervenes` 的反向;**留待落地 PR 在 ADR 0010/0004 关系命名一致性下定**)。
- 即使关联缺位(hook 版本不带 `tool_use_id`),正文事件仍独立可见——挂链是增强,非前提。

### D4. 正文存储沿用 ADR 0005 D2(artifact store + 内容寻址 hash,短文 inline),叠加脱敏与体量上限,**不**与 ADR 0003 去重

- `bytes < 1 KB` inline 到 `payload.after.body_text`;否则文本进 §7 artifact store,payload 只载 `body_sha256`。与 ADR 0005 D2 compaction summary 完全同款,零新增机制。
- 正文过 redaction 管线(与 thinking / compaction summary 同等,ADR 0005 D6),再决定 inline/artifact。
- **不**与 ADR 0003 的静态 `SKILL.md` 快照去重:验证已证正文非字节相同(运行时 header + arg 插值),去重需逐次 diff 静态定义,给 fail-soft reader 平添脆性与跨事件耦合;artifact store 的内容寻址天然对**完全相同**的正文去重(同 sha256 不重复存),已够。payload 里注释与 ADR 0003 快照的重叠关系即可。

### D5. confidence = `observed`,纯观测、绝不 block、fail-soft

正文在 transcript 里直接可见(非推断),记 `loss_hint.confidence: "observed"`。采集是只读旁路,任何解析失败都降级为"少一个事件",不影响 ingest 与 agent 运行。

## Scope

落地涉及(实现细节挂落地 PR / 代码注释,不在本 ADR 展开):

- `internal/transcript/reader.go`:`parseLine` 识别 `isMeta + sourceToolUseID` 的 user 条目并抽正文;新增针对该形态的单测样本。
- `cmd/agent-lens-hook/claude.go`:`claudeHookInput` 增 `ToolUseID`;`makeToolCall` 落 `tool_use_id` ref。
- `internal/ingest/handler.go`:`validKinds` 不变(复用 `context_transform`);若 sub_kind 有显式校验则加一条。
- `internal/linking/*`:`tool_use_id ↔ sourceToolUseID` 的关系建链。
- redaction + artifact store:复用 ADR 0005 D2/D6 既有管线。
- SPEC §5 / §10.1:见头部 `修订(待 Accepted)`。

## 后果

**正向**:

- skill 二阶指令从完全不可见提到一手 `observed`,审计可回答"agent 这套流程从哪来"。
- 复用 `context_transform` + ADR 0005 D2 存储,新增表面最小,无新 EventKind / 无新存储机制。
- 与 gap 1(#111)的 `payload.skill` 判别符经 `tool_use_id` 对接,调用标记 + 正文成完整一对。

**代价 / 风险**:

- 首次让 transcript 路径采集 `user`-role 正文内容(此前只抽 `<system-reminder>`),扩大了对非稳定 transcript schema 的依赖面——靠 D2/D5 的 fail-soft 红线兜底。
- `isMeta:true` 的 user 条目可能不止 skill 正文一类(Claude Code 内部也用 isMeta 标其它注入);`sourceToolUseID` 指向 `Skill` 工具是关键过滤条件,落地需核对是否有其它工具复用同字段而误采。
- 8KB/次量级,长 session 多次 skill 调用会累积 artifact;内容寻址去重缓解,但需在落地时抽样观察实际体量。

## 落地

本 ADR 为草案。Accepted 后按头部 `修订` 改 SPEC,实现按 Scope 分步落地(建议:先 reader 抽正文 + 事件产出,再 hook `tool_use_id` + linker 挂链,两步各自可独立验证)。dogfood 现成链路(本仓库自身的 `/self-review`、`/review` 调用)即天然测试样本。
