# ADR 协作指南

本目录是 Agent Lens 的**架构决策档案**(Architecture Decision Records)。新协作者读这一份就够,不必从 0001 起把所有 ADR 翻一遍。

## 三类文档,各司其职

| 文档 | 路径 | 回答 | 是否可改 | 数量 |
|---|---|---|---|---|
| **SPEC** | `/SPEC.md` | 系统**现在**是什么 | 持续修订,反映当下 | 1 份 |
| **ADR** | `/docs/ADR/NNNN-*.md` | **为什么**当时这么决定 | Accepted 后**不再修改**,只能被新 ADR 取代 | 多份,append-only |
| **Patch(临时)** | `/docs/ADR/spec-patches-*.md` | 如果某 ADR 被接受,SPEC 该改成什么 | 仅在草案窗口存在;接受合入时**和 SPEC 改动同 commit 删除** | 通常 0 份;多 ADR 同发时偶现 |

口诀:**SPEC 说"是什么";ADR 说"为什么";Patch 说"如果接受、SPEC 该改成什么",合入即销毁。**

为什么这样切?——SPEC 必须保持精炼可读,塞进所有历史决策的辩论会让它越读越长;ADR 反过来必须冻结,改了就失去"决策当下的真实考量"这个价值。两个文档目标互斥,所以分开。

## 何时该写 ADR

写:

- **不可逆或难以逆转**的技术选择(语言、wire 格式、存储后端、签名方案)。例:0001 选 Go。
- **当前事实可能改变同事直觉**的决定。例:0002 决定不存 cost——后来者很容易问"为啥不存,加一下啊",ADR 一句话挡住。
- **挑战 SPEC 既有约束**的修订。例:0002 撤回了 v0.4 §10.1 关于 token 不可抓的限制。
- **新增 EventKind 或 schema 变更**(Agent Lens 强 schema,这块改动尤其要可追溯)。例:0003/0004/0005。

不必写:

- 只影响一个 package 内部、不动 SPEC、不动 schema 的实现选择。
- 显然的(既有惯例的延伸:加一个新 SQL migration、加一个 Lens UI 组件)。
- 修 bug、补测试、改 lint。

如果犹豫"要不要写 ADR",标准是:**别人三个月后会不会回来问"为什么这样"**。会问就写。

## ADR 文件结构(模板)

参照 `0001-tech-stack.md` 与 `0002-token-usage-and-cost.md` 的格式。新建文件命名 `NNNN-kebab-case-title.md`,NNNN 取下一个未使用的四位编号。

```markdown
# ADR NNNN:简短标题(中文)

- 状态:草案 | Accepted | 取代于 ADR NNNN
- 日期:YYYY-MM-DD
- 取代:— | ADR NNNN
- 修订(待 Accepted):SPEC §X、§Y          # 仅草案阶段;Accepted 时改成实际修订记录或挪到"后果"段

## 背景

为什么现在需要这个决定。哪些事实 / 压力 / 缺口触发了它。

## 验证(可选)

如果决定基于实测数据(transcript 抽样、benchmark、代码探查),把样本与方法写明。
不要写"我觉得";要写"我看了 N 份 transcript,X / Y 满足条件,以下是结论"。
ADR 0002 § 验证 是范本。

## 决定

### D1. 主决定的一句话表述

理由展开。

**否决备选**:同时把考虑过的备选写在 D 段内联,**否决理由必须给**——
"我们没选 Y 因为 Z"比"我们选了 X 因为它好"信息密度高得多。

### D2. ...

## Scope(本 ADR 范围)

明确**这条 ADR 是文档,还是带代码改动**。Agent Lens 的惯例是 ADR **只做文档**,
落地实现挂到具体 milestone 子项,落地 PR 引用本 ADR 编号。

末尾固定一句:"**本 ADR 不带任何代码改动**"。

## 后果

接受这个决定后,世界长什么样:

- SPEC 哪几段会改(精确到 § 编号)
- 哪些后续 PR / ADR 会基于此
- 数据增量、性能影响、运维负担
- 与既有 ADR 的关系
- 显式留给后续 ADR 的事项(避免本 ADR 越权)
```

更详细的 Style 约定:

- **打号到 D**:决定段落用 D1 / D2 / ... 编号,事后引用方便("ADR 0003 D4 说...")。
- **"否决备选"内联**:不另起一段,放在对应 D 后面。这样读决定时备选就在视野内。
- **句号 / 逗号用中文全角**(`。` `,` `、`),括号 / 冒号 / 分号用半角(`()` `:` `;`)。
- **状态字段**:草案阶段用"草案"中文,接受后用"Accepted"英文。沿用既有惯例,不强制理由。

## 决策接受流程

ADR 状态从 **草案 → Accepted** 必须**原子完成**:同一个 commit / PR 里同时做完三件事——

1. ADR 头部 `状态:草案` 改 `状态:Accepted`,`修订(待 Accepted):` 行删除或合并到 § 后果。
2. 应用 SPEC 改动(直接改 SPEC.md 对应段落 + 版本号 minor bump + 版本注追加一句)。
3. 如有临时 patch 文件(见下),同 commit 删除。

**不允许**先合入 ADR Accepted 状态、SPEC 改动留到后续 PR——这会让 ADR 与 SPEC 漂移,任何一刻读两个文档都给出矛盾答案。回看 0001 / 0002 的 git log 都是单 PR 原子接受。

## Patch 文件:多 ADR 同发的临时机制

**默认情况下不需要 patch 文件**。单 ADR 接受走"ADR + SPEC 同 commit"即可——0001 / 0002 都没用过 patch。

仅当**多份 ADR 草案同时存在、且都改 SPEC 同一组段落、可能被独立接受**时,才引入临时 patch 文件作为合并视图。例:0003/0004/0005 都改 §5 / §7 / §10.1 / §17,且接受不一定同步,这时维护 `spec-patches-pending-0003-0005.md` 让 SPEC 改动可叠加、可分项接受。

Patch 文件的硬性约束:

- 文件名 `spec-patches-pending-NNNN[-NNNN].md`,NNNN 列出涉及的 ADR 编号。
- 内容**按 SPEC 段落组织**(不是按 ADR 组织),每段标"来源:ADR XXXX D Y"。
- 末尾必须有**按 ADR 拆分**的合入清单,接受任一 ADR 时按对应清单条目机械执行。
- **接受流程结束时,本文件与 SPEC 改动同 commit 删除**——不允许长期存活。

如果你只草案了一份 ADR,直接在 ADR 头部 `修订(待 Accepted)` 字段列要改的 SPEC 段,接受时同 PR 改 SPEC,**不要**为单份 ADR 引入 patch 文件——徒增层级。

## 常见坑

- **想小修一份已 Accepted ADR**:不行。开新 ADR 标 `取代:NNNN`。Append-only 不绕过。
- **ADR 和 SPEC 同时写、各写一半**:很容易出现 ADR 说一套、SPEC 说另一套。坚持"原子接受",草案窗口让 SPEC 不动。
- **把实现细节塞 ADR**:ADR 是决策档案,不是实现文档。具体 Go package 路径、SQL 表设计、proto 字段名只在 § Scope 段以"落地涉及"列点提及,详细实现挂在落地 PR 与代码注释里。
- **ADR 之间循环引用**:0003 引用 0004,0004 又引用 0003,读者绕晕。规则:同一组里指定一份"主 ADR",其它在 § 后果 段引用主 ADR 即可,主 ADR 自身不反向引用。
- **"未来某天再写 ADR"**:决策当下不写,事后没人记得当时的备选与权衡。**先写一份草案标"草案",哪怕只有 § 背景 + § 决定 D1**——之后迭代比事后从零造容易得多。

## 现有 ADR 索引

实时索引以本目录文件列表为准(`ls docs/ADR/`)。状态查头部第一行。当前(2026-05-01)摘要:

- **0001 v1 技术栈**(Accepted):Go / Postgres / MinIO / React / sigstore-go 的根决策。
- **0002 把 token 用量纳入证据链**(Accepted):TokenUsage shape;cost 显式不做。
- **0003 Agent 配置快照与 capture-time attestation**(草案):新增 `agent_config_snapshot` EventKind。
- **0004 把 human_intervention 升为头等事件**(草案):新增 `human_intervention` EventKind 与 `Link.relation = intervenes`。
- **0005 把 context 变换升为头等事件**(草案):新增 `context_transform` EventKind。
- **spec-patches-pending-0003-0005.md**:三 ADR 接受时对 SPEC 的预合并补丁,接受合入即删。

## 参考

- 模板范本:`0002-token-usage-and-cost.md`(背景 / 验证 / 决定 / Scope / 后果俱全,推荐新 ADR 直接对照仿写)。
- ADR 文化的英文起源:Michael Nygard 2011 年那篇 *Documenting Architecture Decisions*。本仓库变体:中文撰写、§ 验证段加权、Patch 文件作为多 ADR 同发的工具。
