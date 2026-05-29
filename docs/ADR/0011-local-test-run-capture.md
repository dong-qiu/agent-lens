# ADR 0011:本地测试执行的采集(填 `test_run` 的空产出方)

- 状态:草案
- 日期:2026-05-29
- 取代:—
- 修订(待 Accepted):SPEC §10.1、§15

## 背景

`EVENT_KIND_TEST_RUN`(=8)早在 `proto/event.proto:49` 就定义了,`internal/ingest/handler.go:50` 的 `validKinds` 也接受 `test_run`——**但全仓库没有任何产出方**。这是个隐性盲区:schema 留了位、UI / query 看上去支持"测试执行"这一类,实际上一条都不会写进来。

现状下测试执行的可见性:

- **远端 CI**:GitHub `workflow_run` webhook 派生 `build` 事件(`internal/webhooks/github/mapper.go:242`),把"CI 工作流跑了"作为整体记录,但**不区分**其中的测试步骤,也拿不到 pass / fail / 用例数。
- **本地** `make test` / `go test` / `npm test`:只作为 `Bash` 工具的 `tool_result` 以**不透明文本**落库(`claude.go:252`)。无法结构化查询"这个 commit 前本地跑过测试吗、过了吗、跑了多少用例"。

于是证据链在"测试通过"这个节点是断的。审计问"这次提交前测试过吗、结果如何",答不出——而"agent 改完代码、声称测试通过"正是最该被验证的一类声明(参 §17 dogfood 与 `self-review` skill 的存在动机)。本 ADR 决定**本地测试执行的采集机制**,把 `test_run` 从空 schema 位变成有数据的事件。

## 验证

代码探查(2026-05-29,本仓库):

- `test_run` 仅出现在 `proto/event.proto:49`(enum 定义)与 `internal/ingest/handler.go:50`(校验白名单);`rg '"test_run"'` 在 hook / webhook 产出路径下**零命中**——确认无 emitter。
- `cmd/agent-lens-hook/claude.go:252-269` 的 `makeToolResult` 对所有 `PostToolUse` 一视同仁,仅 `Bash` 命令额外抽 `git:<sha>` ref(`git_commit_ref.go`),不识别测试命令。

本地测试调用的可观测通道:

| 通道 | 能看到命令 | 能看到结果 | 改用户工作流 | 结果置信 |
|---|---|---|---|---|
| `PostToolUse`(Bash) | 是(`tool_input.command`) | 部分(`tool_response` 含 exit / stdout,需解析) | 否 | inferred |
| git `pre-push` hook | 否(只知道要 push) | 否 | 需装 hook | — |
| 显式 wrapper `agent-lens test -- <cmd>` | 是(精确) | 是(自跑自读 exit) | 是(每次手动包裹) | observed |

**仍未决、落地前必须抓样核对**:

- 各测试运行器的 `tool_response` 实际形态——exit code 是否如实透出(`go test`、`pytest`、`jest`),还是被管道 / `tee` / `; echo` 掩盖;摘要行格式。这是 D1 结果解析的事实基础,**未实测**。
- 命令首 token 在真实 `tool_input.command` 中的位置(是否常被 `cd x && `、`env A=B ` 等前缀包裹),供 D1 的锚定规则校准。

落地 PR 按 ADR 0002 同款记录覆盖度。

## 决定

### D1. 用 `PostToolUse`(Bash)识别测试命令,派生 `test_run`,与 `tool_result` 共存

`makeToolResult` 在 `tool_name == "Bash"` 时,除既有 `git:<sha>` 抽取外,再对 `tool_input.command` 跑测试运行器识别;命中则**附加**一条 `test_run` 事件(与 `tool_result` 共存,不替代)。

- **识别(必须锚到命令首 token)**:剥掉前缀后取**实际执行的首 token** 匹配内置测试运行器表(`go test`、`make test` / `make test-integration`、`npm/pnpm/yarn test`、`pytest`、`cargo test`、`jest`、`vitest`、`ctest` 等)。需剥/递归的前缀分两类:(a)**透明包装器**——`cd … &&`、`env A=B`、前导变量赋值、`sudo`/`time`/`nice`/`nohup`、`xargs`,剥到其后的真实命令首 token;(b)**运行器启动器**——`npx`/`pnpm exec`/`yarn dlx`、`poetry run`/`pdm run`/`uv run`、`bash -lc "…"`/`sh -c "…"`,取其后第一个参数 / 引号内首 token 再匹配。这两类硬编码在识别表里,随发版扩(D5)。
- **必须拒绝的假阳**:首 token 是 `echo` / `cat` / `grep` / `rg` / `printf` / `git`(如 `git log --grep=pytest`)等**非执行测试**的命令,即便其参数里出现 runner 名,一律不发 `test_run`;runner 名出现在引号字符串内(非 `bash -c` 那种确为执行体的情形)或 `#` 注释内同样不发。理由见下方否决备选。
- **结果解析**:从 `tool_response` 的 exit code + stdout 抽 pass / fail / 用例数(`go test` 的 `ok` / `FAIL` 行、jest / vitest 摘要行等)。**只有解析出明确结果时才填 `passed` / `failed` / `outcome`**;解析不出(或 exit code 可能被管道掩盖)则**只记 `ran=true` + 原始 `exit_code`,不臆造 pass/fail**,`confidence=inferred`。
- **payload**:`runner`、`command`(已脱敏,见 D4)、`exit_code`、`passed` / `failed` / `skipped`(仅解析成功时present)、`duration`(可空)、`cwd`、`confidence`。

**否决备选(子串匹配)**:像早期设想那样只对 `command` 做 runner 名子串匹配。`echo "run go test before pushing"`、`cat test_output.log`、`grep FAIL test.log`、`git log --grep="pytest"` 都会命中,发出一条 exit=0 的幻影 `test_run`——在审计里被读成"**测试通过了**"。这不是"多一行",而是**伪造了一个裁决**,恰是本系统该防止的事。故识别必须锚首 token 且显式拒绝上述命令族。
**否决备选(git pre-push)**:只在 push 边界记录。只覆盖 push 时刻、漏掉本地反复跑;且 `pre-push` 时测试早跑完,拿不到结果。

### D2. session 归属用 Claude session,附 `git:<sha>` ref 让 linker 缝到 commit

`test_run` 复用 `PostToolUse` 所在的 Claude `session_id`(它本就是 agent turn 内的动作),并在能从 `cwd` 解析出 HEAD 时附 `git:<sha>` ref(复用 `git_commit_ref.go` 同款逻辑),让 linking worker 把 `test_run` 缝到对应 `commit` / `push` 事件。

**否决备选**:为测试另起独立 git-session(像 `git.go:127` 的 `gitSessionID`)。会把 turn 内动作割裂到另一条 session,反而切断"这个 turn 里 agent 跑了测试"的因果。

### D3. 不引入新 EventKind

`test_run` 已在 `proto/event.proto` 与 `validKinds`,本 ADR **只填产出方**,无 schema 变更、无 proto bump。这也是把它单列 ADR 而非顺手实现的理由:采集机制(启发式 vs wrapper vs git hook)是个"三个月后会被问为什么这样"的决定,值得记;schema 本身没动。

### D4. `test_run` 与其源 `tool_result` 同一 §12 脱敏姿态,不单独脱敏

`test_run` 的 `command`(及解析出的输出片段)与它共存的 `tool_result` 是**同一次 Bash 调用的两个视角**。`claude.go` 现有策略对 tool-call **命令**有意不脱敏(为审计可复现,`claude.go:16-21`)。`test_run` **沿用同一姿态**:`command` 保持 verbatim,与 `tool_result` 一致。

**不**对 `test_run` 单独脱敏——这一点经评审纠正了早期草案:单独脱敏 `test_run.command` **既不减少 store 级泄露面**(明文孪生始终在共存的 `tool_result` 里,D1),又制造同一条命令"一处脱敏、一处明文"的不一致审计记录。`test_run` 的泄露边界由 §12 对 tool 命令 / 输出的**统一**姿态治理;§12 若将来收紧对 tool 输出的脱敏,`test_run` 与 `tool_result` **同步**跟随,不在本 ADR 单独设一道半截的脱敏。

**否决备选**:像早期草案那样只脱敏 `test_run`、保留 `tool_result` 明文(类比 0005 D6 的 summary 脱敏)。该类比不成立:0005 D6 明确**丢弃** summary 的 raw(无明文孪生),而 `test_run.command` 按 D1 **必然**有明文孪生共存——脱敏投影买不到真实的泄露收益,只换来不一致。

### D5. 识别表内置 + 注释化扩展,`confidence` 承载"识别可能不全"

测试运行器识别表硬编码在 hook 内,新增 runner 改表即可(注释说明)。不做运行时可配置——配置面是新故障源。识别与解析的不确定性统一由 `confidence` 字段对审计端显式声明。`confidence=observed` 仅留给 D1 否决备选里的 wrapper-CLI 高保真路径(自跑自读 exit);**经 PostToolUse 派生的 test_run 一律 `inferred`**——v1 本地 `test_run` 没有 observed 级。

### D6. 与 `build` 的关系:视角不同,不重复

`build`(CI `workflow_run`)是"CI 工作流整体跑了";`test_run` 是"一次测试执行的结果"。CI 里跑测试,首版只产出 `build`(webhook 派生 CI 侧 `test_run` 留作后续,非本 ADR);本地跑测试只产出 `test_run`。两者来源不同、粒度不同,不构成重复。

## Scope

本 ADR 只做文档。落地涉及:

- `proto/event.proto`:**不变**(`test_run` 既有)。
- `cmd/agent-lens-hook/claude.go`:`makeToolResult` 增测试识别(首 token 锚定 + 包装器/启动器剥离 + 假阳拒绝)+ 结果解析 + 派生 `test_run`(返回多事件,沿用 `buildEvents` 已支持的多事件返回形态);`command` 姿态与 `tool_result` 一致(verbatim,见 D4)。
- 测试运行器识别 / 结果解析表(新文件,hook 内)。
- `internal/linking/`:复用 `git:<sha>` ref 把 `test_run` 缝到 commit / push。
- GraphQL + Lens UI:timeline 显示 `test_run` 节点(pass / fail / 仅 ran 三态色阶),session 维度聚合需对 `confidence=inferred` 显式标注、不当作精确通过率。

**本 ADR 不带任何代码改动。**

## 后果

- §10.1 Hook 直采路径新增一条:"测试执行(`PostToolUse` Bash 首-token 识别派生 `test_run`,结果可解析时带 pass/fail/计数、否则仅 ran,`confidence=inferred`)"。
- §7 `EventKind` 不变(`test_run` 既有,本 ADR 只激活它)。
- §15 增三条风险:(1)**所有 v1 本地 `test_run` 裁决均为 inferred**——识别 / 解析启发式可能漏识别(`./run-tests.sh`、`make ci`、`tox`、`gradle test`、`dotnet test` 等非常规 runner 假阴)或解析错位(exit code 被管道掩盖);审计端不得把 `test_run` 当 observed 真值。(2)漏识别造成的**静默欠计**(无 `test_run` ≠ 没跑测试,也可能是 runner 未被识别)需在 UI / 查询语义上显式区分。(3)**目标名说谎的假阳**:首-token 启发式看不进 Makefile / 脚本内部,`make test` 若实际跑的是 lint,会被记成一次"测试"——首-token 方案无法消除此类,只能由 `confidence=inferred` 兜底声明。
- 数据量:每 session 0–N 条,取决于跑测试频率;dogfood 下中等。
- 重放幂等:`test_run` 经 hook transport at-least-once 通道可能随 `replay` 重复;这是既有 hook 路径通性,session 维度聚合须按 (session_id, command, ts 窗) 去重,避免双计通过率。具体去重归 ingest 既有机制。
- 与 ADR 0002 的关系:`test_run` 不带 `usage`(测试执行无 token 语义),与 token 链路正交。
- attestation predicate(§11):`test_run` 是否进入未来 predicate 版本(把"测试通过"纳入 commit ↔ turn 证据链)留给 attestation 修订决策,本 ADR 不越权收纳——尤其 inferred 级的本地 `test_run` 不宜直接进 attestation。
- 显式留给后续:CI 侧 `test_run`(从 `workflow_run` / check_run webhook 解析单测试步骤)、wrapper CLI 的 observed 级路径(D1 否决备选)——均不需新 EventKind,后续按需各起小 ADR 或挂落地 PR。
