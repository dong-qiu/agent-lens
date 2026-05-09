# ADR 0008:Sub-agent 自动 link fallback（v0.1 K1 实证记录）

- 状态:草案
- 日期:2026-05-09
- 取代:ADR 0007 D3/D4 在 v0.1 范围内的"自动 emit `delegates` link"承诺(D5 已显式预留 fallback,本 ADR 把 fallback 落地)
- 修订(待 Accepted):SPEC §10.1 已知局限段加一句

## 背景

ADR 0007 D3/D4 锁定了"父会话的 PreToolUse(Agent)→子 sub-agent SessionStart"自动 link 的设计,候选 D3-A(env var 直传)与 D3-C(tool_result payload 回填)留作开放问题等实施 PR 实测。本 ADR 记录实测结果与 v0.1 K1 的取舍。

## 验证

实施 PR(K1)期间在 dogfood session 上跑了实测,结论汇总:

### D3-A:hook 改 env var 直传给 sub-agent —— **不可行**

- agent-lens-hook 是 Claude Code 派生的 short-lived 子进程,其 `os.Setenv` 只影响 hook 自身 + hook 自身派生的子进程
- Hook 不 fork 派生 sub-agent,sub-agent 是 Claude Code 父进程派生的
- Claude Code 的 hook 协议(已知)不接受 stdout 里的 env 注入指令——`decision` / `additionalInput` 等控制字段是 tool gating 用,不能改 env
- 结论:无 Claude Code 内部协议改动的前提下,D3-A 路径在 hook 进程层面**理论不通**

### D3-C:tool_result payload 含 child session_id —— **部分可行**

- 实测 14 条 Agent TOOL_RESULT 事件,`payload.response` 含:
  - `agentId`(17 字符 hex,如 `a56565bd5a1a9677f`)
  - `prompt` / `outputFile` / `status` / `description`
- **但** `agentId` ≠ sub-agent 的 hook session_id
- Sub-agent 的 hook 事件用 Claude Code 生成的独立 UUID(如 `319dec3d-87b0-47c9-...`)作 session_id
- agentId 与 session_id 之间**没有任何 surface 出来的映射通路**

### 自动 link 在 v0.1 范围内不可行的核心原因

要从 parent 的 tool_call/result 反查到 child 的 session 事件,需要:

(1) Claude Code 给 sub-agent SessionStart hook payload 加一条 `parent_agent_id`,或
(2) 沿 outputFile 路径反向解析(脆,需文件系统约定),或
(3) 时间戳启发式相关(脆,并发派生时坏)

(1) 需上游 Claude Code 改;(2)(3)都是脆策略。v0.1 不引入这类不可靠路径。

## 决定

### D1. v0.1 K1 走 ADR 0007 D5 fallback

Sub-agent 在 audit 中以**独立 session** 出现(cwd 在 worktree 子目录时,经 `agent-lens-hook setup --personal`(C / #82)装的 user-global hook 触发)。**父→子不自动连边**。Audit reader 通过下面 D2 暴露的元数据 + 时间戳 + prompt 文本人眼对应。

### D2. UI 在父侧暴露 sub-agent 派发元数据

EventCard 对 `kind=TOOL_RESULT`、`payload.name="Agent"` 的事件加一个 🤖 chip,显示截断的 agentId + status,tooltip 显示完整 agentId / status / outputFile。

数据已经被 hook 完整捕获(`payload.response.agentId` / `payload.response.prompt` / `payload.response.outputFile` / `payload.response.status`),只是 UI 没显式 surface。本决定让 audit reader 一眼看到"这条 TOOL_RESULT 是 Agent 派发"而非淹没在通用 tool_result 渲染里。

### D3. linker 不加 `delegates` 关系(超出 ADR 0007 D4 的承诺降级)

ADR 0007 D4 承诺加 `delegates` 关系。本 ADR 在 v0.1 范围内**撤回**该承诺:没有 agentId↔session_id 映射,linker 也无可靠规则去 emit。强行加规则只会产生不可靠的边或空边。`intervenes`、`produces` 等既有关系不受影响。

v0.2 加回的路径在 issue #85(linker: auto-link parent→child sub-agent)追踪,候选有 (A) Claude Code 上游改 / (B) 文件系统 marker / (C) 时间相关 / (D) hybrid。

### D4. SPEC §10.1 已知局限段补一句

实施 PR 同 commit 加:"sub-agent 派发(Agent 工具)在 v0.1 中:父侧 tool_call/result 已捕获完整元数据(含 agentId);子 session 在 user-global hook 装好的前提下独立捕获;父→子的自动 link 留 v0.2(详见 ADR 0008 / 追踪 issue #85)。"

### D5. ADR 0007 不修订

ADR 0007 仍保留 D3/D4 原文与 D5 fallback。它记录了**当时的设计意图**与"未实测前的保守路径"。本 ADR 0008 是"实测后的取舍"。两 ADR 配套读完整。0007 进入 Accepted 时,本 ADR 0008 会同步进入草案修订或独立 Accepted(取决于 v0.2 的进展)。

## 后果

- v0.1 ship 时 sub-agent 链路是"父侧完整 + 子侧独立 + 不 link"。Audit 工具核心承诺(transparency)的 90%+ 满足:**所有派发事件都被记录**,只是 link 需手动重建。
- ADR 0007 D4 的 `delegates` 关系**未引入** linker——不污染 v1 关系词表
- v0.2 的修复路径需要决定 (A/B/C/D),issue #85 是入口
- audit-report 在 v0.1 中,如果 root 是 parent commit 而 sub-agent 写了 commit,两条 chain 会作为独立 session 出现在报告里,**通过 git ref 而非 delegates link 串接**——这刚好是既有 linker 的工作模式,无需特殊处理

## 替代方案

- **v0.1 推迟所有 sub-agent transparency 工作**:ADR 0007 已 Accept(草案),C(setup --personal) 已 ship 解锁了子 session 捕获;现在再回退太浪费。否决。
- **v0.1 引入文件系统 marker 路径**(D3-B):脆策略,引入文件系统约定后未来收回成本高。否决。
- **v0.1 引入时间相关启发**:并发场景错连边比不连边更坏。否决。
- **v0.1 等 Claude Code 上游加 parent_agent_id**:无 ETA,阻塞 v0.1。否决。

## 后续工作

1. **本 PR**(K1):D2 的 UI chip + D4 的 SPEC 更新一句话 + 本 ADR 落档
2. **v0.2 ADR**(待开):基于 issue #85 选 (A/B/C/D) 路径,落 `delegates` link
3. **撤回 ADR 0007 D4 不需另起 ADR** —— 本 ADR 0008 已显式承担"v0.1 范围内的撤回"角色。0007 D4 在 v0.2 落地时由那个 ADR 重新激活或重新定义

**本 ADR 不带任何代码改动**(实施代码在同 PR 的其它文件)。
