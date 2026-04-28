# ADR 0003:PR Review Bot — scope 与 posture

- 状态:Proposed
- 日期:2026-04-28
- 取代:—
- 修订:SPEC §M2(PR Review Bot 条目)、§8、§9.2

## 背景

SPEC §8 把 "PR Review Bot" 摆在 linked event graph 的三个下游消费者之一(和 Lens Web UI、Attestation Exporter 并列)。§9.2 流程步骤 3 勾画了 outbound 的预期动作:bot 在 PR 上发一条 "Evidence Summary" 评论回链 Lens UI。§M2 当初把它列为里程碑交付。

`main`(commit `949bd9c`)上的实际状态:

- **Inbound 已落地**。`internal/webhooks/github` 接收 `pull_request` / `pull_request_review` / `push` / `workflow_run` 4 类 webhook,挂上 `git:<sha>` ref,linker 用它把 PR session 串到产生 commits 的 Claude Code session 上。
- **Outbound 是空的**。没有任何代码调 GitHub API、没有任何代码对 PR 做判断、本仓库不发起任何 LLM 调用。§8 的 bot 框 + §9.2 的步骤 3 描述的是一个二进制里**不存在**的意图。

`PR Review Bot` 这个名字本身也带语义负担:在更广的社区里它通常指**LLM 驱动的自主审查者**(CodeRabbit / Greptile 等)。在没刻意决策的情况下默认这层涵义,会让 Agent Lens 悄悄站到一个**与两条 pinned 决策冲突**的立场上:

- **§13:自托管优先,必须支持 air-gap**。LLM 驱动的 bot 默认开,要么强制运维方提供 LLM endpoint(开箱无用,本质上是 opt-in),要么内置托管 LLM 依赖(打破 air-gap)。
- **§1 / §2**:项目核心价值是**透明与审计**,即**观察**人 ↔ Coding Agent 这一回路。一个**介入** PR 的 bot(自动 approve / reject、对内容做判断)是另一个产品类别,需要明确论证、不能默认延伸。

本 ADR 在任何 outbound 代码动手之前,先把 scope 和 posture 拍下来。

## 决定(提议中)

把 v1 的 "PR Review Bot" 收窄成**最小可行的 Evidence Summary commenter**。具体:

1. **它做什么**:在 `pull_request` 的 `opened` / `synchronize` 事件上,bot 在 PR 上发(或 upsert)一条评论,内容包含:
   - Lens UI 中对应 PR session 的链接(`?session=github-pr:<owner>/<repo>/<n>`)。
   - 当 M3 audit report 对应该 code session 已生成时,附 report 渲染产物 + hash chain head 的链接。
   - 抓到的事件计数概览(例如 *"7 prompts → 23 tool calls → 4 commits, hash chain head abc123…"*)。
2. **它不做什么**:不调 LLM、不出 review 判定、不做 `approve` / `request_changes`、不写 inline 文件评论、不开 GitHub Status Check / Check Run。
3. **鉴权**:单个 GitHub App 安装 token;运行时由 App 私钥 mint(私钥与 collector 同机部署,v1 不上轮换基础设施)。
4. **触发**:bot 订阅已有的 `pull_request` webhook 流 —— 不开独立 poll loop。每次 PR head 推送都重跑;**幂等**靠隐藏 marker(`<!-- agent-lens:evidence-summary -->`)做 comment upsert。
5. **失败模式**:GitHub API 不可达或 App token 误配 → bot 记日志后丢弃这次工作。Inbound webhook ingest 不受影响;审计链不受影响。
6. **配置**:**仅当** `AGENT_LENS_GH_APP_ID` 和 `AGENT_LENS_GH_APP_PRIVATE_KEY_PATH` 同时设置才启用。Air-gap 部署不设这两项,**不损失任何它本来就不需要的能力**。

显式的 v1 非目标:

- LLM 驱动的 PR review(对代码质量 / bug / 风格的自主评论)。如果将来重新考虑,**单开 ADR**。
- Status check 拦截 PR merge(需要先就"什么算 *fail*"达成一致)。
- 跨 PR 推理(例如"这个 PR 回滚了另一个的 deploy")。这是 linker 的活,不是 bot 的活。

## 本 ADR 拍板的决策点

| # | 问题 | 拍板答案 |
|---|---|---|
| D1 | bot 功能边界 | **仅评论**。Evidence Summary 回链 Lens UI + audit report。不出判定、不写 inline review。 |
| D2 | 触发 | `pull_request` webhook(`opened`、`synchronize`)。无 polling。 |
| D3 | LLM 接入策略 | **v1 不接 LLM**。评论由事件库数据(计数、head hash、链接)模板填充。未来 LLM review = 单独 ADR。 |
| D4 | 成本 / 限流 | 每个 PR push 事件最多 1 次 comment upsert。无 LLM 即无按事件计费。GitHub API 限流走标准 client 退避;不需要 token 预算。 |
| D5 | 不可信输入 / prompt-injection | **v1 不在 scope** —— bot 不喂 PR 内容给任何 LLM。等 LLM review 落地时,那个 ADR 必须解决 sandbox。 |
| D6 | 与 audit report 的关系 | bot 是 `agent-lens-hook export` / M3-D audit report 输出的**消费者**。评论链向已渲染产物;不重新派生其内容。 |
| D7 | 优雅降级 | bot 通过 env 变量 opt-in。没配 GitHub App 凭据时 ingest 照常工作,只是 outbound 那一侧关闭。Air-gap 不受影响。 |
| D8 | GitHub App 鉴权 | App ID + 私钥落在 collector 主机,运行时按需 mint installation token。**v1 不上外部 KMS**;轮换由运维触发。 |

## 评估过的备选

- **B:v1 直接上 LLM 驱动的自主审查者**。表面收益最高,但 (1) 除非走 opt-in(那就退化回提议方案),否则破 SPEC §13;(2) 在向公开 PR 发 LLM 输出之前必须先有真正的 prompt-injection 威胁模型;(3) 拖入厂商选型(Anthropic vs OpenAI vs 自托管),每条都有自己的 §13 含义。**v1 否决,但不永久排除** —— 如果 audit-report 链接基线证明信号不够,未来 ADR 可以把它作为 opt-in 模块加上来。
- **C:无限期延后,把 §8 / §9.2 里的 "PR Review Bot" 删掉**。诚实但放弃了一个小而稳的胜利(把 PR 链回审计证据链),否决。
- **D:用 Status Check / Check Run 替代评论**。和 PR merge 闸门集成更好,但永远报 "neutral / informational" 的 check 会污染 checks 面板而无信号增量。**评论的好处是它响在该响的位置**。v1 否决;等真有可以 gate merge 的可执行内容时再考虑。
- **E:用 Go 之外的语言写**。给部署多塞一个 runtime,人体工学收益不抵 —— 现有 collector 二进制已经会说 GitHub webhook 协议。否决。

## 后果

- **SPEC §M2 更新**:已落地的两个 bullet(在 `949bd9c` 加的)留着;outbound bullet 里的"需先单开 ADR-0003"被本 ADR 满足。当本 ADR 状态变 `Accepted` 后,outbound bullet 可以提升为 M4(或落到任何确定里程碑)的具体交付项。
- **SPEC §9.2 步骤 3 保留** —— "PR Bot 在 PR 上评论一段 Evidence Summary 链接到 Lens UI" —— 正好对应本 ADR 的 v1 scope。
- **§8 架构图保留**;"PR Review Bot" 框继续作为 Query API + M3 audit exporter 的下游消费者位置。
- **CLAUDE.md "Pinned decisions"** 加一行引用本 ADR,防止后续编辑无意中重新讨论 LLM 这条。
- **本 ADR 不带任何代码改动**。实现以本 ADR 被 Accepted 为门槛。被 Accepted 后工作拆成:
  1. 新 `internal/ghapp/` 包:App-token minter、comment upsert client、幂等 marker。
  2. 在 collector main 里接入,env-gate。
  3. 在已有 `pull_request` webhook handler 完成 ingest 之后触发。
  4. 集成测试基于录制的 fixture(不打活的 GitHub)。

## 留给后续 ADR

- **ADR-0004(假设)**:LLM 驱动的 PR review 模块。任何 outbound LLM 调用上线前**必须**先有它。须包含:厂商选型理由、prompt-injection 威胁模型、opt-in 语义、LLM 不可达时的失败模式、每个 PR 的 cost / token 预算。
- **ADR-0005(假设)**:GitHub App 凭据轮换 / 多租户。仅在 Agent Lens 超出单 installer 规模时才需要。
