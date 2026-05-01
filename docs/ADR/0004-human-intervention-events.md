# ADR 0004:把 human_intervention 升为头等事件

- 状态:草案
- 日期:2026-05-01
- 取代:—
- 修订(待 Accepted):SPEC §5、§7、§10.1、§17

## 背景

SPEC §5 在 actor 层有 `type=human`,但**人在协同过程中具体做了什么**没有专属事件 kind 来承载。当前已捕获事件里隐式包含一些人类动作(commit 是人 author 的、PR 是人创建的),但下面这些更细粒度的人类决策被吞掉:

1. **权限提示决策**:Claude Code 触发 `PreToolUse` 时,permission 系统弹出 "Allow / Allow always / Deny / Edit" 选项。人选了什么、是否改写工具参数——`PreToolUse` hook payload 里能拿到,但当前没事件类承载,被折叠进 tool_call 事件本身,deny 这条**关键负信号**事后查询不出来。
2. **打断 / 撤回**:人按 ESC、改写半截 prompt、撤回工具调用前的批准。今天只在最终未发生那一刻被间接观察(PreToolUse 之后没有 PostToolUse)。
3. **手工修改 agent 提案**:agent 改完代码,人在 commit 前进 IDE 改了几行——commit diff 包含 agent 与人的混合贡献,但归因被压扁到一个 git author 上。
4. **PR 评审决策**:人 approve / request changes / "merge over reviewer block"(包括未来 AI reviewer 的 block)。GitHub webhook 已入库 `pull_request_review` 但 override 语义没有显式表达。
5. **越权升级**:审计场景需要的不仅是"事情发生了",而是"谁授权了它发生 / 越过了什么 gate"——permission allow_always 与 settings 里的 permissions 黑白名单变更尤其。

这一批信号的共同点:**它们是人对 agent 行为的反馈**,正负都关键。负反馈(deny / interrupt / request_changes)尤其稀缺——agent 按什么 prompt 行动今天能看,被打断在哪今天看不见。

## 验证

按可获取性排查现有捕获通道(2026-05 时点):

| 类别 | 当前可获取性 | 通道 |
|---|---|---|
| Permission allow / deny + 用户改写 | **能** | Claude Code `PreToolUse` hook payload 中 permission decision 字段 |
| ESC 打断 | **部分能** | `Stop` hook 触发,但"被打断"vs"自然结束"无明确区分;transcript 末尾 `stop_reason` 可补判 |
| Prompt 改写后重发 | **能** | `UserPromptSubmit` hook 每次都触发,序列可还原 |
| IDE 直接修改 agent 提案 | **不能** | 没有 IDE 插件层,commit diff 是混合的 |
| PR 评审 actions | **能** | GitHub webhook `pull_request_review` 已订阅(M2 已交付) |
| Approve over reviewer block | **能** | `pull_request_review` + `pull_request` merge event 时序拼接可推断 |

5 类中 4 类**今天就能落地**,第 4 类(IDE 直接编辑)需要 IDE 插件层,留给 M4+。本 ADR 立 schema、覆盖前 4 类,manual_edit sub_kind 先预留字段位、暂不发射,以避免未来 IDE 插件接入时再 bump schema version。

落地实施前应对一份真实 Claude Code session 的 `PreToolUse` hook payload 抓样,核对 permission decision 字段的实际形态(允许/允许永久/拒绝/编辑后允许)与是否携带改写后的工具参数 diff。这条假设在 ADR 写作时**未实测**。

## 决定

### D1. 新增 EventKind `human_intervention`,sub_kind 走 payload

```
HumanIntervention {
  sub_kind: "permission_decision" | "interrupt" | "prompt_revision"
          | "review_decision" | "merge_override" | "permission_config_change"
          | "manual_edit"                       // 见 D4,字段位预留
  target_event_id?:  ULID                       // 被介入的 agent 事件;prompt_revision 可空
  target_artifact?:  artifact_id                // PR / commit 时填
  decision:          "allow" | "allow_always" | "deny" | "edit"
                   | "approve" | "request_changes" | "comment"
                   | "interrupt" | "override"
  rationale?:        string                     // 用户填的或从 review body 抽出来的
  modified_payload?: object                     // edit 类的"改写后"内容
  surface:           "claude_code_hook" | "github" | "ide" | "cli"
  raw?:              object                     // 原始来源载荷,留给取证
}
```

`target_event_id` 与 §7 `Link` 配合:除了载入 payload,linking worker 还要为本事件生成 `Link{from=human_intervention, to=target_event, relation=intervenes}`。trace 视图能从被介入的 agent 动作回溯到对应人类决策。

**否决备选**:每个 sub_kind 独立 EventKind。会膨胀 enum 且各 sub_kind 共用大量字段(target、surface、rationale),独立 kind 没换来什么。

### D2. permission_decision 与 tool_call **共存**,不替代

`PreToolUse` 当前已经入 tool_call(pending)事件。新事件 `human_intervention.permission_decision` **附加**在该 tool_call 之后,via `target_event_id` 链回。

为什么不合并:tool_call 事件描述"agent 想做什么",permission decision 描述"人允许做什么"。两个动作在审计上语义不同——"agent 试图执行 X 但被拒绝"是关键的负信号。如果把 deny 折叠进 tool_call 而不发独立事件,事后查询"被拒过哪些"就要扫所有 tool_call,而不能直接查 human_intervention 表。

`allow` 也发,虽然看起来冗余(没人介入也是默认 allow)。理由:`allow_always` 与一次性 `allow` 在审计上必须可区分;`allow_always` 是一次"对未来无数次同类动作的预批",这个**授权范围**事后必须能看到。

### D3. interrupt 事件由 Stop hook 推断,需要 stop_reason + 时间窗启发式

Claude Code 没显式 "interrupt" 信号。判断规则:

- transcript 最后一条 assistant 消息的 `stop_reason == null` 或缺失,**且** 该消息后未跟 `tool_result`(即 agent 中断在工具调用前/中),则 Stop hook 发 `human_intervention.interrupt`。
- 反之 `stop_reason ∈ {end_turn, tool_use, stop_sequence, max_tokens}` 时正常关闭 turn。

启发式可能假阳/假阴。`raw` 字段保留 transcript 末尾原始 stop_reason 与最后两条消息的 timestamp,以便事后复盘。

**否决备选**:等 §10.4 代理深模式拿真实 SIGINT 信号。能更准但 v1 不上线,启发式作为占位足以。dogfood 高频 ESC 中断场景下,假阳率可观察,过高就上 §10.4。

### D4. manual_edit 字段位预留,首版不发射

IDE 插件层未到位前,agent 改代码 → 人手改 → commit 的混合归因无法还原。schema 里保留 `manual_edit` sub_kind 与 `code_change.contributor_mix`(按 hunk 的 author 归因)字段位作为协议占位,避免未来 IDE 插件接入时再改 schema。

§17 dogfood 也吃这亏:作者经常人手微调 agent 提案。这条限制写入 §17 已具备捕获深度的注释里(待 Accepted 时同步)。

### D5. review_decision / merge_override 走 GitHub webhook 既有路径,语义强化

`internal/webhooks/github` 已订阅 `pull_request_review`(M2 交付)。本 ADR 要求该 handler **额外发出**对应的 `human_intervention.review_decision` 事件,与既有 `review` event **共存**(冗余,但低成本——见 D7)。

PR merge 事件入库时,handler 回扫该 PR 上是否存在 `request_changes` 状态的 review;存在则在 merge event 之后追发 `human_intervention.merge_override`,target = 该 review event。

未来 AI reviewer(见上一轮讨论中的 reviewer-as-AI 设想)接入后,sub_kind 不变——`override` 的语义对人和 AI reviewer 是一致的,只是 actor 类型不同。这条**有意**让 schema 与 actor type 解耦,unifies 人和 AI reviewer 的 override 路径。

### D6. permission_config_change 单独 sub_kind,settings 黑白名单变更触发

permissions 黑白名单(`settings.json` 中的 `permissions.allow / deny / ask` 段)每次变更都是一次"人对未来 agent 行为的策略性授权"。ADR 0003 的 `agent_config_snapshot` 会在配置漂移时发新事件,但那是 bundle 级 hash 变化,不区分**改的是什么**。

本 ADR 要求:0003 的 configbundle 发射器在检测到 `settings.permissions` 段 hash 漂移时,**额外**发一条 `human_intervention.permission_config_change`,payload 中含 diff(添加/删除/修改的 entry list)。这是面向审计的细化视图,与 0003 的 bundle 快照互补。

**否决备选**:依赖 0003 bundle diff 在 query 层重算。实现简单但每次 query 都要 diff 两个 bundle,且"谁改了 permissions"这种问题应该是头等可查的,不该藏在 bundle 内部。

### D7. rationale 字段非强制,query 层暴露"无 rationale 的 override"为审计审视项

要求人填理由会被绕开(瞎填字符)。所以 rationale 是 optional 字段,但 GraphQL 提供 `humanInterventions(missingRationale: true)` 查询,让审计端能主动找出无理由的 override / deny / merge_override。这是策略而非强制。

### D8. 与既有 `review` EventKind 的关系:共存,不删旧

§7 现有 `EventKind` enum 已含 `review`,与 GitHub `pull_request_review` 1:1 对应。本 ADR 不删除 `review`——它承载"评审作为 PR 生命周期事件"的视角(GitHub 来源、与 PR artifact 直接挂钩)。`human_intervention.review_decision` 承载"评审作为人对 agent 行为的反馈"视角,两者**视角不同、字段重合**,冗余可接受。

linker worker 在收到 `review` 事件时**自动派生**对应的 `human_intervention.review_decision`,reviewer 不需要操心这层一对一关系。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:增加 `EVENT_KIND_HUMAN_INTERVENTION`;payload 用 Struct。
- `internal/capture/permission/`:从 `PreToolUse` hook payload 抽 permission decision。
- `internal/transcript/`:interrupt 启发式扩到 Stop hook 处理。
- `internal/webhooks/github/`:补发 review_decision 与 merge_override。
- `internal/capture/configbundle/`(与 ADR 0003 共享):检测 permissions 段漂移并发 permission_config_change。
- `internal/linking/`:生成 `relation=intervenes` link。
- GraphQL + Lens UI:timeline 用独立色阶显示 human_intervention 节点;`humanInterventions(missingRationale: true)` 查询。

**本 ADR 不带任何代码改动**。

## 后果

- §5 的 actor 列表保留 `human`,但 `human` 下的具体动作由 `human_intervention` 事件承载——文档需在 §5 明确这点。
- §7 `EventKind` enum 长一个值,既有 `review` 不变。`code_change.contributor_mix` 保留为字段占位。
- §10.1 hook 路径下 4 / 5 类已可捕获,manual_edit 显式标记"等 IDE 插件层(M4+)"。
- §15 R1(thinking 落盘)独立,不涉及。R2(linking 准确度)受益——human_intervention 事件提供强归因锚点(target_event_id 显式)。
- AI reviewer 场景的对接路径已经预埋:reviewer 的 approve / request_changes 走同一 EventKind 但 actor.type=agent;merge_override 不区分被 override 的是人还是 AI reviewer。这条让未来 AI reviewer ADR 不需要再 bump schema。
- 数据增量预估:每 session 5–30 条 human_intervention 事件(permission decisions 主导),比 tool_call 量低一个数量级。
- attestation predicate(§11)暂不收纳 human_intervention。`code-provenance/v1` 关注 commit ↔ turn ↔ prompt 链路,human_intervention 在该链路之外提供横向证据,是否在未来 predicate 版本里 surface 留给 attestation 修订决策。
- 与 ADR 0003 / 0005 的关系:三者新增 EventKind 互不冲突。0003 是 session/turn 级配置锚点,0004 是 turn 内人工动作,0005 是 turn 间 context 变换。permission_config_change 是 0003 与 0004 在 schema 层的桥。
