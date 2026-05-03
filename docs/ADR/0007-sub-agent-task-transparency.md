# ADR 0007:Sub-agent(Task 工具)透明度

- 状态:草案
- 日期:2026-05-03
- 取代:—
- 修订(待 Accepted):none —— 本 ADR 在 §10.1 hook 路径既有事件类型上加一条 `Link.relation` 与一种新的链接产生途径,不改 SPEC §6/§7 数据模型;实施 PR 落地 §10.1 时同 PR 对 SPEC §10.1 已知局限段补充一句即可,无需打 SPEC patch。

## 背景

本仓库 §17 dogfood 已激活,M1–M3 链路工作。但本周一次实测暴露了一处**结构性捕获缺口**:在为 ADR 0006 的 release 准备工作中,4 个 Claude Code sub-agent(经主会话的 `Task` 工具派生)各自完成了实质性工作 —— 起草 0006 草案、修复一处 `verify-attestation` 引用、跑了一次 overclaim 扫描。事后查 live agent-lens server,这 4 个 sub-agent session **零事件落库**。

根因二选一,且互相叠加:

1. **hook 配置位置**。本仓库目前在 `.claude/settings.local.json`(项目本地)注册 hook,而 Claude Code 派生 sub-agent 时给的 cwd 在 `.claude/worktrees/agent-XXX/` —— 这个目录不是 git 工作树根,不携带 `.claude/settings*.json`,Claude Code 的 settings 解析不会向上爬到主仓库根去捞配置。结论:**项目本地配置对 sub-agent 永久不生效**。
2. **session_id 缺归属**。即使 sub-agent 触发了 hook,主会话不知道哪一条 Task 工具调用对应哪一个 child session_id,timeline 上看到的是两条孤立的 session,没有"主→子"边。审计读起来等同于"两件不同的事"。

SPEC §1 把 Agent Lens 定位成"面向 Coding Agent 的透明可审计系统"。Claude Code 的 sub-agent 是头等的 Coding Agent 行为(它会写代码、跑工具、产生 commit),v0.1 不能让这块漏掉。本 ADR 钉死 v0.1 的捕获策略,把"主会话→sub-agent"关系升为 `Link.relation` 词表里的一员,显式不解决的部分(UI 嵌套渲染、兄弟 sub-agent peer link)推到 v0.2+。

## 验证

**本 ADR 不做实证 Claude Code 行为验证** —— 两个核心假设(D3-A 的 env var 时机、D3-C 的 tool_result payload 结构)留到实施 PR 验证;假设不成立时降级路径见 D3-D。

可锚定的事实只有两条:

1. **观察到的零捕获**。本周本仓库一次 release 准备过程中,4 个 sub-agent 的工作在 agent-lens server 中查不到任何事件;主会话该轮的 PreToolUse(Task)记录可见,PostToolUse(Task)的 tool_result 也存在,但下游 child session 完全空白。
2. **配置文件解析行为**。Claude Code 的 settings 合并依赖 cwd 起的"项目根"概念;sub-agent 在 worktree cwd 下没有 `.claude/settings*.json`,这是文件系统事实,不依赖 Claude Code 内部行为假设。

落地实施 PR 必须在至少一份干净 Claude Code 安装上跑过一次"主会话起 Task → 看 child session 是否真的发了 SessionStart 事件",把 D2/D3 中标的"假设"逐条核实,核实结果回写到实施 PR 描述。

## 决定

### D1. Hook 配置默认安装到用户级 `~/.claude/settings.json`

`agent-lens-hook setup --personal`(ADR 0006 D8)默认把 hook 注册写到 `~/.claude/settings.json`,**不写**项目本地 `.claude/settings.local.json`。理由:用户级配置随用户身份漂,与具体 cwd 无关 —— sub-agent 在任何 worktree 起来都看得见。这是捕获 sub-agent 的**必要条件**,不是优化项。

提供 `--project-only` 标志用于"只想给一个仓库开 Agent Lens、不污染全局 hook"的场景,但文档明确警告:**该模式下本仓库的 sub-agent 永久不被捕获**,审计报告会有结构性盲区。这是用户的选择,不是默认。

**否决备选**:同时写两处。两处合并语义由 Claude Code 决定,我们没法保证主→子方向继承的是哪一份;且两处都写会让 `--uninstall` 的"精确移除"语义模糊(用户改了其中一处怎么办)。一锤定音放用户级。

ADR 0006 D8 已规定 setup 子命令"幂等地把本工具的 hook 注册合并进 `~/.claude/settings.json`",本 ADR 与之一致 —— 把"默认"显式化、把 `--project-only` 列为 escape hatch,不改 0006 的方向。

### D2. Child session_id 直接采用 Claude Code 自生成的 UUID

每个 sub-agent 由 Claude Code 派生时**假设**会触发自己的 `SessionStart` hook,带一条新的 `session_id`(沿用主会话同款 ULID/UUID 格式)。hook 把这个 id 当作 child session 的规范 id 直接存,不做派生命名。

**假设**:Claude Code 为每次 Task 工具的 sub-agent 调用都生成独立 `session_id`,且 sub-agent 内部触发 hook 时该 id 与父会话不同。**验证留给实施 PR**;实测不成立时降级到"hook 端用 `<parent>::task-<n>` 派生命名",但同 PR 内补一份 ADR 0007 修订说明,不静默切换。

**否决备选**:hook 端派生 `<parent>::task-<n>`。需要 hook 自己能识别"我现在在 sub-agent 上下文里",这个识别依赖比"接受 Claude Code 给的 id"更深的内部假设。复杂度倒置。

**否决备选**:hybrid(Claude Code id + human-readable alias)。alias 没有审计价值,只是 UI 糖,留给 Lens UI 自己渲染时拼,不污染事件 schema。

### D3. 主→子链接机制:env var 直传 + tool_result 兜底,二选一即落

候选机制:

- **(A) env var 直传**。主会话的 PreToolUse(Task)hook 在事件落库后,把 event id 通过 `AGENT_LENS_PARENT_EVENT_ID` 环境变量传给即将启动的 sub-agent 进程;sub-agent 的 SessionStart hook 读到该变量,把它写进自己的 SessionStart 事件 payload。linker 看到 child SessionStart payload 携带 parent event id,emit `delegates` 关系。
  **开放问题**:hook 在 PreToolUse 阶段能否修改将传给 sub-agent 进程的环境?抑或 Claude Code 派生 sub-agent 的进程已经在 hook 触发时 in-flight,环境已定型?**留给实施 PR 验证**。
- **(B) 文件系统 marker**。父写一个文件,子在 SessionStart 读。**否决**:路径协调易撞,worktree 隔离场景下父子未必看见同一文件系统视图,竞态条件多,工程脆弱。
- **(C) tool_result payload 事后回填**。主会话的 PostToolUse(Task)在 sub-agent 返回时触发,Claude Code 给出的 tool_result payload **可能**含 child session_id;若有,父端 hook 直接 emit 一条 `Link{from=父PreToolUse事件, to=子SessionStart事件, relation=delegates, inferred_by=tool_result_payload}` 兜底。
  **开放问题**:Claude Code 的 Task tool_result payload 是否含 child session_id?**留给实施 PR 验证**。
- **(D) hybrid**:同时实现 A 与 C,任一可用即生效,linker 端 dedup(同一对父子事件 emit 多次 `delegates` 走 `ErrDuplicate`,见 `internal/linking/linker.go` 的现有处理)。

**采纳 (D)** 作为本 ADR 的规范决定,实施 pragma 是"先做能跑通的那一半,另一半做兜底"。两条路径都依赖未验证的 Claude Code 内部行为,任何一条单独被证伪 hybrid 仍能跑;两条都被证伪时降级到"sub-agent 在 timeline 上以独立 session 出现,不与父连边",由 §17 dogfood 在产品层判定是否阻断 v0.1 发布。

实施 PR 在做 A/C 实测时,可在主会话起一个仅含 `echo "$AGENT_LENS_PARENT_EVENT_ID" > /tmp/probe.txt` 的最小 sub-agent prompt 来核 A;在主会话 PostToolUse(Task)handler 里把整个 `tool_response` raw payload 落到 `~/.agent-lens/debug/` 下一份样本文件来核 C 的字段结构。两个探针都不阻断主流程、不污染产线事件流。

### D4. 新增 `Link.relation = "delegates"`,不复用 `references`

SPEC §7 现有 relation 词表为 `produces / references / reviews / builds / deploys`(linker 端额外有 `intervenes`,见 ADR 0004 草案)。新增 `delegates`:**主会话 PreToolUse(Task)事件 → sub-agent SessionStart 事件**这条边的语义。

为什么不复用 `references`:语义不同。references 表"事件 A 提到了某个 ref(commit、artifact),与同 ref 的事件 B 共享该锚点";delegates 表"事件 A **派生**了一个新的执行上下文,B 是该上下文的入口"。审计差异具体到一句话:"this commit references session XYZ" vs "this commit was delegated to sub-agent XYZ" —— 第二句话才能让 reviewer 决定要不要顺着边追下去看 sub-agent 的 prompt 历史。security review 角度,delegation 关系是信任传递点,reference 不是。

落地代价:linker `relation.go` 加一个常量字符串与一条 `InferRelation` 规则(kind=tool_call 且 tool_name=Task → kind=decision 且 marker=session_start ⇒ delegates);GraphQL `Link.relation` 已是 `String!`,前端不需要 schema 改动;Lens UI 把 `delegates` 与既有 relation 共用 chip 即可,不强求新视觉。

**否决备选**:把 delegates 当 `references` 的一种 confidence>0.9 子类。审计 SQL filter 写起来需要 `relation = 'references' AND inferred_by LIKE 'task_%'`,字符串匹配做不可靠承诺,且与 SPEC §7 词表枚举的可枚举性相悖。

### D5. 显式不在 v0.1 范围(延后 v0.2+)

为了让未来读者不疑惑"这条 ADR 为什么没做 X",留档:

- **Lens UI 嵌套 sub-agent 时间线 / 因果图渲染**。v0.1 中 sub-agent 在 SessionList 里以平铺独立 session 出现,只有列表上方显示一个"delegated by ←"的小 link chip 即可;真正的 nested timeline / collapsible tree 留给 v0.2。
- **递归 sub-agent 捕获深度 > 3**。理论上 sub-agent 能再起 sub-agent。v0.1 在 hook 端硬上限 depth=3,超出深度记一行 stderr warning 并继续派生(不阻断 Claude Code 行为),不 emit `delegates`。深度上限的产品理由是"4 层以上几乎一定是配置错误或失控循环",不是 schema 限制。
- **同父兄弟 sub-agent peer linking**。同一主会话的多个 Task 调用之间是否需要 sibling link(`co_delegated`?)留给 v0.2,因为目前没有审计场景需要"兄弟"这层语义。
- **基于 policy 的 sub-agent 派生拦截**。"在 PreToolUse 时按 policy 阻断某些 Task 调用"属 §10.4 代理深模式范畴(需要实时拦截能力),与本 ADR 的捕获语义独立,由独立 ADR 类拍板。

## Scope

本 ADR 只做文档与 schema 决策。落地涉及:

- `cmd/agent-lens-hook/setup.go`:`setup --personal` 默认写 `~/.claude/settings.json`(D1);新增 `--project-only` flag。
- `cmd/agent-lens-hook/claude.go`:SessionStart handler 读 `AGENT_LENS_PARENT_EVENT_ID` env(D3-A);PreToolUse(Task)handler 把自身 event id 通过子进程 env 传出;PostToolUse(Task)handler 在 tool_result payload 含 child session_id 时 emit 父→子 link 兜底(D3-C)。
- `internal/linking/relation.go`:加 `RelationDelegates` 常量与对应 `InferRelation` 规则(D4)。
- `internal/linking/linker.go`:接受 SessionStart payload 中的 parent event id 作为新链接产生途径,与现有 ref-based 路径并存。
- SPEC §10.1 已知局限段:加一句对应文案(实施 PR 同 commit 改)。

**本 ADR 不带任何代码改动**。

## 后果

- §10.1 已知局限段在实施 PR 中追加一句:"sub-agent(Task 工具派生)捕获依赖用户级 hook 注册;`--project-only` 模式下不抓 sub-agent。" 不需要 SPEC patch 文件,实施 PR 同 commit 改 SPEC 即可。
- ADR 0006 D8 的 `setup --personal` 措辞与本 ADR D1 一致(默认用户级),不需要 0006 修订;若实施 PR 中发现 README 的 setup 演示需要补一句"sub-agent 透明依赖此默认",在该 PR 内顺手补上即可,不另起 ADR 修订。
- linker 词表多一条 `delegates`,与 ADR 0004 草案的 `intervenes` 同模式 —— linker 端定义新 relation、Lens UI 在 chip 渲染端兼容字符串。两者互不冲突。
- 数据增量:每个 sub-agent session 与单人 session 同量级(prompt + tool_call/result + thought + decision);本仓库 dogfood 期估算每周 + 5–20 个 sub-agent session,~2–10 MB / 周,与 ADR 0003 同数量级,artifact 去重后更低。
- 与既有 ADR 关系:ADR 0003 的 `agent_config_snapshot` 在 sub-agent 的 SessionStart 上同样应该发(sub-agent 的工作目录、permission mode 可能与父不同)—— 这是 0003 实施 PR 的份内事,本 ADR 不再重申。

## 替代方案

- **完全放弃 sub-agent 捕获,等 §10.4 代理深模式上线再说**。被否决:§10.4 是 M4+ 的事,且 v0.1 personal mode 已经发布前夕;让 v0.1 出门时带"sub-agent 永久看不见"这种已知盲区,与 SPEC §1 的"透明可审计"承诺直接冲突。
- **强制 hook 配置改回项目本地 `.claude/settings.local.json`**。被否决:见背景段第 1 条,sub-agent cwd 在 worktree 下根本读不到项目本地文件,这条路被文件系统事实关死。
- **每个 sub-agent 启动时由父进程显式 inject 完整 hook 配置(子继承 hook 字典)**。被否决:这要求 hook 二进制反过来去操纵 Claude Code 的子进程派生,侵入性远超 v0.1 范围,而且需要假设 Claude Code 暴露派生时机的钩子点 —— 没有证据它暴露。
- **delegates 改名 `spawns`**。被驳回(更偏好 delegates):`spawns` 暗示进程模型,delegates 暗示语义模型("这一段决策被外包出去");Agent Lens 是审计工具,语义层用语优先。

## 后续工作

按 D1..D5 拆分的实施 PR 清单,每条独立可 review:

1. **实施 PR(D1+D2+D3+D4)**:`agent-lens-hook setup --personal` 默认写用户级 + `--project-only` flag(D1);child session_id 直接采用 Claude Code 给的 id(D2);D3-A env var 直传机制 + D3-C tool_result 兜底机制,实测后保留能跑的部分(D3);linker 加 `delegates` relation 与对应 `InferRelation` 规则(D4)。同 PR 在 SPEC §10.1 已知局限段加一句对应文案。
2. **README/ADR 0006 措辞校对(可能不需要)**:实施 PR 完成时核对 README 的 setup 演示与 ADR 0006 D8 措辞,若与"默认用户级、`--project-only` 是 escape hatch"叙事不一致再补丁;若已一致(很可能)则跳过。
3. **v0.2 后续 ADR(不在本 ADR 范围)**:Lens UI 嵌套 sub-agent timeline 渲染设计;同父兄弟 sub-agent peer link 语义;§10.4 代理深模式拍板时附带的 policy gate 对 sub-agent 派生的拦截策略。

**本 ADR 不带任何代码改动**。
