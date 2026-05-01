# ADR 0003:Agent 配置快照与 capture-time attestation

- 状态:草案
- 日期:2026-05-01
- 取代:—
- 修订(待 Accepted):SPEC §5、§7、§10.1、§12、§17

## 背景

SPEC v0.5 的事件 schema 已经把 prompt / thinking / tool_call / tool_result / token usage 串成证据链,但 agent **决策时身处什么配置之下**几乎没有头等记录。三条现实压力把这个缺口顶成 M3 之后第一优先项:

1. **同一 prompt 行为不可预测**:`~/.claude/CLAUDE.md`、`<repo>/CLAUDE.md`、`<repo>/.claude/CLAUDE.md`、`<repo>/.claude/settings*.json`、permission mode、注册的 hook、加载的 skill / agent / command 文件——任何一个改动都会让同一 prompt 产出完全不同的行为。事后审计如果没有 turn 时刻的"指令栈快照",回答不了"agent 这次行为合不合规"。

2. **采集器自身是信任空白**:M3 `agent-lens verify` 保护事件**序与完整性**,但不保护"事件源真伪"。如果 `agent-lens-hook` 二进制被静默替换、`settings.json` 被改、MCP server 名义未变实质换内容,证据链上面那一截就是空白,但 hash chain 仍然 verify 通过。

3. **dogfood 漂移频繁**:§17 已激活,本仓库自身的 SPEC.md / CLAUDE.md / hook 经常改。每改一次都改变后续 turn 的行为基线,但这种"配置漂移"在当前 schema 下不可见,timeline 上看不到分水岭。

## 验证

不展开样本验证,先列 Claude Code 在 SessionStart hook + 文件系统直读路径下**已经能拿到**的字段,作为本 ADR 实现路径的下界:

| 来源 | 字段 | 是否需要新捕获通道 |
|---|---|---|
| SessionStart hook payload | `cwd`、`session_id`、`source`、`hook_event_name` | 否 |
| 文件系统直读 | `~/.claude/CLAUDE.md`、`<repo>/CLAUDE.md`、`<repo>/.claude/CLAUDE.md` | 否 |
| 文件系统直读 | `<repo>/.claude/settings.json`、`settings.local.json`、`~/.claude/settings.json` | 否 |
| 文件系统直读 | `<repo>/.claude/agents/*.md`、`commands/*.md`、`skills/*` | 否 |
| transcript jsonl | `message.model`(每条 assistant 消息) | 已用(ADR 0002) |
| **未覆盖** | active MCP server 列表、本轮 tool catalog、permission mode 即时值、thinking budget、temperature | **是** |

最后一行是核心限制:Claude Code 没有暴露"本轮工具空间"的 hook。`PreToolUse` 能拿到被调用的工具,拿不到**未被调用、但本可被调用**的工具集合。这条留给 §10.4 代理深模式或 IDE 插件,本 ADR 显式让它在 bundle 里 `unknown`,见 D4。

落地实现开工前需要按 ADR 0002 同款做法,在至少一份不同操作系统(macOS / Linux)的 Claude Code 安装上核对文件解析顺序与合并语义,把假设与缺口写进实施 PR。

## 决定

### D1. 新增 EventKind `agent_config_snapshot`,载体走 artifact store

每次 SessionStart 起一条 `agent_config_snapshot` 事件,payload 含**配置 bundle 的 content hash**(指向 artifact store 中的实际内容)+ 一段最小 metadata 摘要(agent identity、permission mode、参与合并的文件路径列表、`unknown_fields`)。

bundle 本体(指令文件 hierarchy、settings*.json、agents/、commands/、skills/、hook 二进制 hash 列表)走 §7 现有 artifact store,事件 payload 只引 hash。

**否决备选**:bundle 当 inline JSON 塞 payload。复用既有内容寻址 artifact store 是免费的,inline 会让 events 表平均行宽涨一个数量级。

### D2. Bundle 由 hook 在出口处合并并归一化,不依赖 server 重建

bundle 内容(首版):

```
ConfigBundle {
  schema_version:       int
  collected_at:         timestamp
  agent: {
    type:               "claude-code"
    cli_version:        string                     // claude --version
    binary_sha256:      string                     // 从 PATH 解析定位的 claude 可执行
  }
  instructions: {                                   // 按 Claude Code 实际加载顺序
    files: [{
      path:             string                     // 解析为相对 project root 或 ~/
      content_sha256:   string
      bytes:            int
    }]
  }
  settings: {
    files: [...]                                   // settings.json + settings.local.json + ~/.claude/settings.json
    effective_permission_mode: string              // plan / acceptEdits / bypassPermissions / default
    effective_hooks: [...]                         // 解析合并后的 hook 注册列表
  }
  capture: {                                        // 见 D3
    hook_binary_sha256: string
    hook_version:       string
  }
  tool_space?:    null                              // §10.1 路径不可得,见 D4
  model_params?:  null                              // 同上
  raw_files: <opaque>                               // 上面所有 file 原文 tar+zstd,丢进 artifact store
}
```

哈希用 SHA-256,与 §7 现有 artifact id 一致。`instructions.files[].path` 既保留原绝对路径(脱敏 home 段)也写相对路径,两者都参与 bundle hash 计算——审计要能区分"全局 CLAUDE.md 改了" vs "项目 CLAUDE.md 改了"。

**否决备选**:server 端从原始事件流重建。server 看不到 client 文件系统,重建必然丢字段;hook 一次合并好后端只需信任 hash,与 §11 in-toto attestation 的"信封内信任"姿态一致。

### D3. capture-time attestation 与 bundle 同事件入库

把"hook 二进制自身 hash"塞进 `ConfigBundle.capture` 而不另起一个 EventKind。这样**配置快照与采集器自身真伪在同一 hash chain 节点同时定锚**——攻击者改 hook 必然改这条事件的 content hash,与后续 hash chain 链路对不上。

`hook_binary_sha256` 由 hook 二进制启动时对自身 `/proc/self/exe`(macOS 走 `_NSGetExecutablePath` 同等)取 hash 算出,写入 bundle。

M3 `agent-lens verify` 增加一个**可选**检查:把 bundle 中声明的 hook hash 与一份 server 端可信发布清单对比。清单怎么管理(git tag + sigstore 签名? 本地 KMS 签名?)留给 M3 实施时另起 ADR——本 ADR 只保证 hash **被记录、被链上**,不规定如何信任它。

**否决备选**:单独的 `capture_attestation` EventKind。两个事件就需要 link,且大概率永远 1:1 出现,分开只增加复杂度。

### D4. 工具空间 / model 参数在 hook 路径不可得时显式 `null` 加 `unknown_fields`

`ConfigBundle.tool_space` 与 `model_params` 在 §10.1 路径**不可得**,bundle 里这两个键置 `null` 并在 metadata 里 `unknown_fields: ["tool_space", "model_params"]`。审计端看到 unknown 就知道这块要走 §10.4 才能补全,而不是被静默当成"工具空间为空"。

OpenCode 接入(§10.2)或 §10.4 代理深模式上线后这两个字段才会真填。bumping `schema_version` 不需要——optional 字段从 null 变非 null 是兼容变更。

### D5. 发射节奏:SessionStart + 配置漂移触发,不每 turn 重发

- **SessionStart**:必发一条。
- **每个 PreToolUse / UserPromptSubmit hook 启动时**:对**指令文件集合 + settings 集合**重算合并 hash;与上次 bundle 的 hash 不同则发新 `agent_config_snapshot`,target 是当前 turn。Hash 用 D2 中的 bundle hash,与 SessionStart 同算法。
- **Hook 二进制 hash** 不在每 turn 重算(开销 + 噪声),只在 SessionStart 算一次。

预期事件量级:稳定 session 1 条 `agent_config_snapshot`;dogfood 高频改动期 5–20 条。比同 session 的 prompt / tool_call 量低 2 个数量级。

**否决备选**:每 turn 必发。线性放大事件量,且大多 turn 间配置 hash 完全相同。基于 hash 漂移触发已经足够。

### D6. 敏感字段在 hook 出口处脱敏

`settings.json` 可能含 token / API key(Claude Code 自身一般不放,但 third-party hook 不保证)。bundle 在写 artifact store **之前**走 §12 redaction:value 命中模式 `(?i)(token|key|secret|password|bearer|authorization)` 的 key 整字段替换为 `<redacted:sha256:...>`,原文不存。

被 redact 的字段名仍出现在 metadata,因为字段**存在与否**也是审计相关的,只是其值不存。

§15 R1 讨论 thinking 落盘的隐私边界,与本条**同执行点**(hook 出口),实施时合并管线。但策略各自独立——thinking 是否落盘 vs settings value 是否脱敏,是两个产品决定。

## Scope

本 ADR 只做文档与 schema 决策。落地涉及:

- `proto/event.proto`:增加 `EVENT_KIND_AGENT_CONFIG_SNAPSHOT`;payload 用现有 `google.protobuf.Struct`(同 ADR 0002 的 usage shape 处理)。
- `internal/capture/configbundle/`:新包,负责文件收集 / 合并 / 脱敏 / 哈希 / artifact store 上传。
- `internal/agentlenshook/claude/`:`SessionStart`、`UserPromptSubmit`、`PreToolUse` 子命令调用 configbundle。
- `internal/hashchain/verify.go`:增加 capture attestation 的可选交叉验证(hash 清单怎么来留给后续 ADR)。
- GraphQL:`Session.configSnapshots` resolver。
- Lens UI:session 详情页加 "Config" tab,列 bundle 历史 + diff 视图(可复用 PR #44/#46 落地的 Monaco DiffEditor)。

**本 ADR 不带任何代码改动**。

## 后果

- 证据链获得"决策时身处什么配置之下"的头等表达。M3 verify 之上叠加可选"采集器真伪验证",闭合"hook 静默替换"这条威胁。
- §10.1 现有"已知局限"补一条:`tool_space` / `model_params` 在 hook 路径不可得,等 §10.4。措辞与既有 thinking 落盘限制对仗。
- §17 dogfood 受益最直接:本仓库 SPEC / CLAUDE.md / hook 高频修改,引入此 ADR 后 timeline 上能看到每次配置漂移点,这正是"工具观测工具自己演进"的具体兑现。
- bundle 的 schema 演进策略:`schema_version` 字段保留;新字段加 optional 默认 unknown;breaking change 走新 ADR + bump version + Lens UI 双读窗。
- attestation predicate(§11)暂不收纳 capture attestation。是否在 `agent-lens.dev/code-provenance/v1` 或后续版本里 surface 该 hash,留给 attestation 修订单独决策。本 ADR 只保证**事件层**有这条信息,不强制 attestation 层 surface。
- 数据增量(估算):dogfood 全期增量 ~MB 级别,因为指令文件集合大小有限且大量重复 hash 命中 artifact 去重。
- 与 ADR 0004 / 0005 的关系:三者新增 EventKind 互不冲突,linker 把它们拼回对应 session / turn 节点。0003 在 timeline 上是 session-level 锚点(漂移点),0004 是 turn 内人工动作,0005 是 turn 间 context 变换。
