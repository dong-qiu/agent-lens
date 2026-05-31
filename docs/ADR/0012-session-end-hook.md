# ADR 0012:订阅 SessionEnd,补齐会话边界

- 状态:Accepted
- 日期:2026-05-29
- 取代:—

## 背景

会话当前只有开、没有闭。已订阅 `SessionStart`(`claude.go:95`、派生 `decision.session_start`),但**没有 `SessionEnd`**。会话结束只能靠 `Stop` hook 的 `turn_end` marker 推断,而 `turn_end` 是 **turn** 边界不是 **session** 边界——分不清"正常退出 / 清空上下文(clear)/ 退出后又 resume 续接 / 进程崩溃"。

一个 resume 续接的 session 会有多段 turn 串在同一 `session_id` 下,没有 `session_end` 就无法分段,审计端读不出"这里其实断过一次"。这是"Claude Code 提供了 hook、我们没订阅"的现成盲区。

> 本 ADR 原与 PreCompact 合并(草案早期版本)。考虑到 SessionEnd 只复用既有 `decision` marker、**今天就能独立接受**,而 PreCompact 依赖 ADR 0005 的 `context_transform` EventKind 先落地、接受时机不同步,故拆分:PreCompact 移至 ADR 0013。

## 验证

Claude Code 官方 hooks 文档(https://code.claude.com/docs/en/hooks,2026-05-29 抓取核对):

| Hook | 触发时机 | 关键 payload / matcher |
|---|---|---|
| `SessionEnd` | 会话终止时 | matcher 按 **`reason`**:`clear` / `resume` / `logout` / `prompt_input_exit` / `bypass_permissions_disabled` / `other`;不可 block |
| `SessionStart` | 会话开始 / 恢复 | matcher 按 `source`:`startup` / `resume` / `clear` / `compact`;payload 另含 `model`、`agent_type`(可选)、`session_title`(可选) |

关键观察与**修正**:

- `SessionEnd` 的 matcher / 字段是 **`reason`**(早期草案写作 `end_reason`,经文档核对纠正为 `reason`)。
- `SessionStart.source = resume` 与 `SessionEnd.reason = resume` 配对,能还原跨进程的 session 续接。
- `SessionStart` 除 `source` 外还暴露 `model`、`agent_type`、`session_title`——可顺手采下,补强 0003 的 session 锚点(见 D2)。
- `source = compact` 在一次 compaction 之后触发;ADR 0013 已改用专门的 `PostCompact` 作后括号、不再依赖本字段,故本字段在此仅作 compaction 的辅助佐证,非 0013 的必需前置。

**抓样核对结果(2026-05-31,#125 落地前)**:

- ✅ **字段名确认**:真实 SessionEnd stdin payload 的字段就是顶层 **`reason`**(非 `end_reason`、非嵌套),`claude.go` 的 `json:"reason"` 正确,无需改码。
- ✅ **取值**:观测到 `prompt_input_exit`(正常退出)、`clear`(`/clear`),均在文档集合内。
- 📌 **同 session 多条**:同一 `session_id` 跨多次退出 / 续接会发**多条** `session_end`(实测一个 id 出现 3 条 `prompt_input_exit`)——据此修正 §后果"每 session 1 条"。
- ✅ **`reason=resume` 确认**:在活动会话内用 `/resume` **切走到另一段对话**时,被离开的会话发 `session_end` 带 `reason=resume`(实测:新会话切走得 `resume`;被切入的会话随后正常退出得 `prompt_input_exit`)。即 `resume` = "会话被存盘待续"而非终止。
- 进程崩溃 / 被 kill 时不发 `SessionEnd`(预期,hook 没机会跑),会话靠"无 session_end 收尾"反推——已记入 §15 R10。

抓样方法:临时项目级 SessionEnd dump hook(`cat >> …`),触发正常退出 / `/clear` / resume,读原始 payload(ADR 0002 同款覆盖度记录)。

## 决定

### D1. 订阅 `SessionEnd`,派生 `decision` marker `session_end`,镜像 `makeSessionStart`

`buildEvents` 增 `SessionEnd` 分支,派生 `actor=system(claude-code)`、`kind=decision`、`payload.marker=session_end`,带 `reason` 与 `cwd`。形态对称于 `claude.go:271` 的 `makeSessionStart`。

会话从此有显式闭边界;`reason` 让审计端区分"正常退出(prompt_input_exit)/ 清空(clear)/ resume 续接 / logout"。

**否决备选**:继续靠 `Stop` 的 `turn_end` 末条推断会话结束。`turn_end` 是 turn 边界,resume 续接的多段 turn 共享一个 `session_id`,无 `session_end` 无法分段——这正是当前盲点。

**否决备选**:为 `session_end` 新增独立 EventKind。`session_start` 当前就是 `decision` marker(`claude.go:271`),闭边界与开边界同档处理,无需 schema 变更——新增 EventKind 没换来任何区分度。

### D2. `SessionStart` 仅增采 `source`(`model` / `agent_type` 等归 ADR 0003)

`makeSessionStart` 增记 `payload.source`(startup / resume / clear / compact)。这不是新事件,是既有事件的小增强:

- `source=resume` 与 D1 的 `reason=resume` 配对,还原跨进程续接——审计端能把"退出"与"续接"接成一条连续会话史。
- `source=startup` / `clear` 区分全新会话 vs 清空上下文重开;`source=compact` 仅作 compaction 的辅助佐证(0013 的后括号已改用 `PostCompact`,不依赖此字段)。

**只采 `source`,不采 `model` / `agent_type` / `session_title`**:文档显示 SessionStart payload 还含这几项,但它们属于 agent 配置 / 身份,归 ADR 0003 的 `agent_config_snapshot`——0003 在同一 SessionStart 起快照(bundle 含 model 等),且 `makeSessionStart` 今天已把 `permissions` 写进该 payload(`claude.go:280`)。若本 ADR 再把 `model` 松散写进 SessionStart payload,就成了"什么 model"的第二个真相源,与 0003 的配置漂移检测可能打架。`source` 是 session **边界**语义(本 ADR 的 domain),与 0003 的配置快照不重叠,故只采它。

**否决备选**:顺手把 `model` / `agent_type` 一并采下("零成本")。这正是"无害多采一个字段"的反例:当字段落在另一份 ADR(0003)拥有的 payload 上,就制造静默的双写漂移;需要它们时由 0003 的快照承载,不在此重复。
**否决备选**:不动 `SessionStart`。`source` 是 resume 配对的必需,且与 0003 无重叠,采了划算。

### D3. 纯观测、绝不 block、fail-soft

`SessionEnd` 本就不可 block。解析失败 warn 到 stderr、一律 `os.Exit(0)`,与现有所有 hook 分支一致(`claude.go:80`),绝不中断 Claude Code。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:**不变**——`SessionEnd` 复用 `decision` marker。
- `cmd/agent-lens-hook/setup.go`:hook 订阅列表增 `SessionEnd`。
- `cmd/agent-lens-hook/claude.go`:`buildEvents` 增 `SessionEnd` 分支;`makeSessionStart` 增 `source`。
- `internal/linking/`:resume 续接关联(SessionEnd resume ↔ SessionStart resume)。
- GraphQL + Lens UI:timeline 显式 session 闭边界;会话史按 resume 边界分段。

**本 ADR 不带任何代码改动。**

## 后果

- §10.1 Hook 直采列表增 `SessionEnd`;`SessionStart` 采集字段增 `source`(仅此一项)。
- §7 `EventKind` 不变(`session_end` 用 `decision` marker,与 `session_start` 同档)。
- §15:崩溃 / 被 kill 时 `SessionEnd` 不发,会话只能靠"无 session_end 收尾"反推——已加为 §15 R10(并记入实测:正常退出 / `/clear` 发、同 `session_id` 可多条)。
- 与 ADR 0003 的关系:本 ADR**只**增采 `source`(session 边界语义),**不碰** 0003 拥有的 SessionStart 配置 payload(`model` / `agent_type` / `session_title` / `permissions`)——避免对同一 SessionStart payload 的双写漂移。`source` 与 0003 的 bundle 快照正交、同事件、不重叠。
- 与 ADR 0013 的关系:0013 改用 `PostCompact` 作 compaction 后括号后,**两份 ADR 已互相独立**——本 ADR 的 `source=compact` 仅作 0013 的辅助佐证,非其必需前置;双方均可独立接受,无依赖方向。
- 数据量:`session_end` 每次会话退出 1 条;resume 续接的同一 `session_id` 可多条(实测一个 id 3 条),整体仍可忽略。

## 落地

实现挂本 ADR,与接受同 PR:`SessionEnd` 订阅(setup.go)+ `buildEvents` 的 `SessionEnd` 分支与 `makeSessionEnd`(claude.go)+ `makeSessionStart` 增 `source`,并附单测。`internal/linking` 的 resume 续接关联与 Lens UI 会话分段为后续子项,跟踪 issue 另开。
