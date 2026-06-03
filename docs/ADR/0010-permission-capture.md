# ADR 0010:用 PermissionRequest / PermissionDenied 捕获权限请求一手证据

- 状态:Accepted
- 日期:2026-05-29
- 取代:—(修正 ADR 0004 D2 的来源真值声明;D2 的"共存 + via target_event_id 链回"决定不变,见 § 后果)

## 背景

ADR 0004 把 `human_intervention` 立为头等 EventKind,D2 决定 `permission_decision` 子类与 `tool_call` **共存**、via `target_event_id` 链回——这个决定是对的,本 ADR 不动它。问题出在 D2 附带的一句**来源真值声明**:"`PreToolUse` hook payload 里能拿到 permission decision 字段"。0004 自己的 §验证 已经诚实标注:这条假设**在 ADR 写作时未实测**。

现在核对了(见 §验证),结论是**这条来源声明不成立**:

1. `PreToolUse` 在权限对话框**之前**触发,payload 带 `tool_name` / `tool_input` / `permission_mode`,但**不携带用户的 allow / deny 选择**——本质是"调用前钩子",不是"用户决策记录"。
2. 用户的**交互式** allow / deny 选择,**没有任何 hook 以结构化字段直接投递**(`PermissionRequest` 让 hook *代为*决策,不回投用户最终选择;`PermissionDenied` 只覆盖 auto 模式分类器的拒绝)。
3. 真正一手的信号是:`PermissionRequest`(对话框出现、**携带与 PreToolUse 同样的工具信息**)、`PermissionDenied`(auto 模式拒绝)、`PostToolUse` / `PostToolUseFailure`(工具是否真的执行了)。

更糟的是当前代码已在错误前提上跑:`cmd/agent-lens-hook/claude.go:181` 的注释写反了("PreToolUse only fires after Claude Code has granted permission"),并以 `allowlist_match` 是否为空来**反推**授权路径——正是 0004 想消除的"折叠进 tool_call、事后只能反推"的毛病。

本 ADR 把 `permission_decision` 锚到真正的一手 hook 上,并守两条审计底线:**只在权限 gate 实际出现时**(`PermissionRequest` 触发,即真有一次向人 / 分类器请求授权)才发 `human_intervention.permission_decision`;**不臆造任何裁决**——交互式拒绝不可观测时记 `unresolved`,自动放行(无人参与)根本不记为人类干预(见 D1 / D2)。

## 验证

Claude Code 官方 hooks 文档(https://code.claude.com/docs/en/hooks,2026-05-29 抓取核对,以原始页面为准):

| Hook | 触发时机 | 关键 payload / matcher | 能否拿到用户 allow/deny |
|---|---|---|---|
| `PreToolUse` | 权限对话框**之前** | `tool_name`、`tool_input`、`permission_mode`(default/plan/acceptEdits/auto/dontAsk/bypassPermissions) | **不能** |
| `PermissionRequest` | 权限对话框出现时 | **与 `PreToolUse` 同样的 `tool_name` / `tool_input`**(无 `tool_use_id`);可选 `permission_suggestions` | **不能**(hook 代为决策,不回投用户最终选择) |
| `PermissionDenied` | **仅** auto 模式分类器拒绝时(`PermissionRequest` 的旁支) | tool 信息;输出 `hookSpecificOutput.retry`;不可 block | 部分(**只**覆盖 auto 模式拒绝,**不**覆盖交互式 deny) |
| `PostToolUse` | 工具**成功**执行后 | `tool_name`、`tool_input`、`tool_response` | 间接(触发即说明被许可且成功) |
| `PostToolUseFailure` | 工具执行**失败**后 | 同 `PostToolUse` 的 `tool_name`/`tool_input` + 错误信息 | 间接(触发即说明被许可但失败) |

关键观察:

- `PermissionRequest` **携带工具信息**,可直接把权限请求关联到对应 `tool_call`,**无需**早期草案那条脆弱的"最近一条未配对 PreToolUse"启发式(并行工具调用会让它错配)。
- "工具是否被许可且执行"由 `PostToolUse` **或** `PostToolUseFailure` 是否触发确证——二者覆盖"成功"与"失败"两种已执行情形,故**被许可但执行失败**不会被误判为拒绝;`PermissionDenied` 是 auto 拒绝的旁支,与执行路径不混。
- 交互式拒绝(用户在 default/ask 模式点 deny 或 ESC)**没有专门 hook**;`PermissionDenied` 只管 auto 模式。

**仍未决、落地前必须抓样核对**:

- `PermissionRequest` 的完整 stdin payload(确认工具信息字段名与 `PreToolUse` 一致)。
- 一次交互式 deny 后,是否**确实**既无 `PostToolUse` 也无 `PostToolUseFailure`(D2 的"未执行 ⇒ 未许可"推断的前提)。

落地 PR 按 ADR 0002 同款做覆盖度记录:抓一份 `default` 模式下发生过权限对话框(含一次 allow、一次 deny)与一份 `bypassPermissions` 模式的真实 session 核对上表。

## 决定

### D1. **只在 `PermissionRequest` 触发时**派生 `permission_decision`;自动放行不记为人类干预

订阅 `PermissionRequest` 作 `permission_decision` 的一手采集面。**`PermissionRequest` 触发** = 确有一次向人 / 分类器请求授权的 gate;**仅此时**派生 `human_intervention.permission_decision`(0004 D1 的 EventKind 与字段不变),`target_event_id` 按 `PermissionRequest` 的 tool_name + 输入 + session + 时间窗匹配到对应 `tool_call`,并生成 0004 D1 的 `relation=intervenes` link。

`bypassPermissions` / `acceptEdits` / `auto` / `dontAsk` 等模式下策略自动放行、**无 gate、无人参与**——**不**派生 `permission_decision`。这类工具的授权事实由 D3 记在 `tool_call.payload.authorization.permission_mode` 上,而非伪装成一次人类干预。

**否决备选**:对**每个执行过的工具**(含自动放行)都发 `permission_decision{allow}`。自动放行没有人在回路里,把它记成 `human_intervention`(语义是"人做了某事")是 category error;且在 `bypassPermissions`(本仓 dogfood 默认)下每个 Bash/Edit/Read 都发一条,量级仅次于 tool_call 本身,会把罕见的真实人类决策淹没在 auto 噪声里——这与本 ADR 减少噪声的初衷相反。授权事实归 `permission_mode`,不归 `human_intervention`。
**否决备选**:用 `Notification(permission_prompt)` 作主锚。`Notification` 不带 tool 名,且 `PermissionRequest` 已携带工具信息、更精确——`Notification` 仅是冗余佐证,不值得多订阅一个 hook(本 ADR 不订阅它,避免 scope 蔓延)。

### D2. outcome 分档,既不臆造 `deny`、也不把自动放行当人类 `allow`

`permission_decision`(仅在 D1 的 gate 前提下存在)的 `decision` 字段按可观测性分档:

- **被许可(observed)**:gate 出现后对应 `tool_call` 触发了 `PostToolUse` **或** `PostToolUseFailure` ⇒ 人在 gate 上放行、工具确实执行 ⇒ `decision="allow"`、`confidence=observed`(执行与否的成败不影响"被人放行")。
- **auto 分类器拒绝(observed)**:`PermissionDenied` 触发 ⇒ `decision="deny"`、`surface=auto_classifier`、`confidence=observed`。
- **交互式未执行(unresolved,不发裁决)**:gate 出现但其后既无 `PostToolUse` 也无 `PostToolUseFailure` ⇒ 结果不可观测(可能 deny、可能 ESC interrupt、可能进程退出)⇒ `decision="unresolved"`、`confidence=inferred`、`outcome_note` 说明"gate 触发但工具未执行,具体裁决不可观测"。区分 deny vs interrupt 留给 0004 D3 的 `stop_reason` 启发式作*提示性* hint,不升格为 `decision`。

**否决备选(致命,两侧对称)**:(a) 未执行 ⇒ 推断 `deny`——把被打断 / 进程退出伪造成"人类拒绝",负向 inference-as-evidence;(b) 自动放行 ⇒ 记 `allow observed`——把无人参与伪造成"人类放行",正向 inference-as-evidence + 洪泛。两者都拒绝:本系统存在的意义是不让"声称"冒充"证据"。诚实的表达是 `unresolved`(交互未决)与"不发事件"(自动放行)。
**否决备选**:等 §10.4 proxy 拿请求级真值。proxy 更准但 v1 不上线;observed 的 allow / auto-deny + 诚实的 unresolved 已够 v1,未来 proxy 接入只需把 unresolved 升真值,审计端 query 不变。

### D3. `permission_mode` 纳入 tool_call 的 authorization payload,承载自动放行的授权事实,修正错误注释

`PreToolUse` 既然带 `permission_mode`,就记进 `tool_call.payload.authorization.permission_mode`(真值),并**修正 `claude.go:181` 写反的注释**。自动放行工具的"凭什么被允许"由此承载(模式即授权来源),无需 D1 的 `permission_decision`。当前用 `allowlist_match` 空否反推"用户手批 vs 自动"的逻辑(`claude.go:181-203`)降级为辅助——它仍回答"命中哪条 allow 规则",但"是不是 / 谁手批"以 `permission_mode` + D1/D2 为准。

**否决备选**:删掉 allowlist_match 逻辑。它仍回答"命中哪条 allow 规则",正交有用,保留但不再背负它扛不动的语义。

### D4. 纯观测、绝不参与决策、fail-soft

`PermissionRequest` / `PermissionDenied` 可输出决策 / retry,但 agent-lens hook **绝不**参与——一律不输出决策、纯观测、`os.Exit(0)`,与现有所有 hook 分支一致(`claude.go:80`)。绝不改变 Claude Code 的权限流。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:**不变**——复用 ADR 0004 的 `human_intervention` EventKind 与 `permission_decision` sub_kind(EventKind 落地仍挂 0004,目前 Accepted-but-unlanded);`decision` 是 `payload`(Struct)内字符串,增 `unresolved` 值无 proto bump,但属对 0004 D1 值集合的扩展(见 §后果)。
- `cmd/agent-lens-hook/setup.go`:hook 订阅列表增 `PermissionRequest`、`PermissionDenied`、`PostToolUseFailure`(**不**订阅 `Notification`,见 D1)。
- `cmd/agent-lens-hook/claude.go`:`buildEvents` 增对应分支;`makeToolCall` 记 `permission_mode`、修注释;D2 的 outcome 在 `PostToolUse` / `PostToolUseFailure` 配对或 Stop 收尾处判定。
- `internal/linking/`:`target_event_id` 关联与 `relation=intervenes` link(与 0004 共用)。
- GraphQL + Lens UI:与 0004 共用 `human_intervention` 节点;`decision=unresolved` 与 `confidence` 显式透出,deny 审计视图**只**计 observed 的 deny。

**本 ADR 不带任何代码改动。**

## 后果

- §10.1 "事件捕获路径"的 Hook 直采列表增 `PermissionRequest` / `PermissionDenied` / `PostToolUseFailure`;"人工干预"bullet 改写——`permission_decision` 仅在 `PermissionRequest` gate 触发时产出,outcome 由 `PostToolUse`/`PostToolUseFailure` 确证许可、`PermissionDenied` 确证 auto 拒绝、交互未执行记 `unresolved`;自动放行不产出 `permission_decision`,授权事实记在 tool_call 的 `permission_mode`。
- §7 `EventKind` 不变(复用 0004 `human_intervention`)。
- 与 ADR 0004 的关系——**两处对 0004 的修订,均须在 0004 留痕**:
  1. **来源真值更正**:0004 D2 的"`PreToolUse` 携带 decision"声明经本 ADR §验证 证伪;0004 D2 的核心决定(共存 + `target_event_id` 链回)不变。
  2. **`decision` 值集合扩展**:0004 D1 枚举 `allow|allow_always|deny|edit|approve|request_changes|comment|interrupt|override`,本 ADR 增 `unresolved`。
  鉴于 0004 是 **Accepted**(非草案),接受本 ADR 时给 0004 加一行**来源/状态注记**(形如 `状态:Accepted — D2 来源声明经 ADR 0010 §验证 更正;decision 集合由 ADR 0010 增 unresolved`),纠正的**决定本身**落在本 ADR(0010),0004 只做 provenance 注记——这不改 0004 的决定内容,故不违 append-only(注:commit 658b5b4 给 0009 加 needs-revision banner 是同*类*的状态注记,但 0009 是草案、0004 是 Accepted,此处是对 Accepted ADR 状态行的 provenance 注记,略强于该先例,故在此显式说明依据)。
- 与 ADR 0004 D3(interrupt 启发式)的关系:D3 的 `stop_reason` 启发式从"判定 interrupt 事件"降为本 ADR D2 `unresolved` 下的*提示性* hint,不独立升格为裁决。
- 重放幂等 + 顺序:`permission_decision` 的 outcome 在 `PostToolUse`/`PostToolUseFailure` 配对或 Stop 收尾时判定,故晚于它链回的 `tool_call` 入链——正常因果方向(同 ADR 0005 D7),verify 时按 `target_event_id` 校验。hook transport at-least-once 通道可能重复投递,可按 (session_id, target_event_id) 去重。`human_intervention` EventKind 已随 #131(Phase 0)落地;outcome 关联(把 gate 与后续 tool 执行配成 allow,或 turn 收尾无执行配成 unresolved)归 `internal/linking` 后续(见 § 落地)。
- 数据量:`permission_decision` 仅在 gate 出现时产出 ≈ 交互式权限对话框次数。`default` 模式按非 allowlist 工具计(繁忙重构 turn 可达数十);`bypassPermissions` / `acceptEdits` 模式**无 gate ⇒ 0 条 `permission_decision`**(授权事实仅在各 tool_call 的 `permission_mode` 字段)。这修正了早期草案"对自动放行也发 allow"导致的洪泛。
- 审计收益:"谁授权越过哪个 gate"从反推变一手锚点;交互 allow 与 auto-deny 为 observed,交互未决诚实标 `unresolved`,自动放行不冒充人类决策——allow 与 deny 两个审计视图都不被 inferred / auto 噪声污染。
- 显式留给后续:(1)交互式 allow/deny 真值的结构化捕获(等 §10.4 proxy,届时 `unresolved` 升真值,不需新 EventKind);(2)`Notification` 的 `idle_prompt` 等其它类型的分析价值有限,**不在本 ADR 收纳**,如需另起小 ADR。

## 落地

分两步,本 PR 是第一步(hook 一手采集),outcome 关联留 linker:

- **本 PR(hook 采集)**:订阅 `PermissionRequest`(→ `permission_decision` 记 gate 出现、带 target 工具、`surface=interactive`、`confidence=inferred`、**不**填终态 `decision`——待关联)、`PermissionDenied`(→ `permission_decision{decision=deny, surface=auto_classifier, confidence=observed}`)、`PostToolUseFailure`(→ `tool_result`,补齐失败工具 + 给关联"工具跑过"的信号,顺带 0011 失败测试也被 `test_run` 捕获)。`permission_mode` 入 `tool_call.authorization` 并修正 `claude.go` 反了的注释(D3)。`setup.go` + `settings.example.json` 同步订阅(#127 守卫)。
- **linker 后续**:把 gate 的 `permission_decision` 与后续 `tool_result`/失败配对 → `decision=allow`(observed);turn 收尾无执行 → `decision=unresolved`(D2)。`intervenes` link(+ `RELATION_INTERVENES`)、`agent_config_snapshot`(0003)桥接的 `permission_config_change`(0004 D6)均为各自后续。

`human_intervention` EventKind 复用 #131 已落地的 schema;`decision` 集合增 `unresolved` 值(payload 字符串,无 proto bump)。
