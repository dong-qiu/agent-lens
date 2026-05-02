# ADR 0006:v0.1.0 发布形态与分发渠道(personal mode)

- 状态:草案
- 日期:2026-05-03
- 取代:—
- 修订(待 Accepted):none —— v0.1 范围与 SPEC §13 "single-node Docker Compose for MVP" 一致,无需打 SPEC patch。

## 背景

M1–M3 已交付完整证据链闭环(hook → ingest → Postgres → GraphQL → Lens UI,含 linking、in-toto/SLSA attestation、hash chain `verify`、审计报告导出),§17 dogfood 已激活,本仓库自身就是首个使用者。但**对外发布形态还是空白**:

- `README.md` 第 338 行仍写 "`go.mod` 的 `github.com/dongqiu/agent-lens` 是占位"。实际 GitHub 组织是 `dong-qiu`(带连字符)。
- `README.md` 第 342 行 "许可:待定"。
- `.github/workflows/ci.yml` 只跑测试,没有 release workflow,没有 binary artifact,没有签名,没有 container 推送。
- `script/install-dogfood.sh` 假设用户能 `git clone` + `go build` 起步 —— 这是开发者路径,不是用户路径。

继续把这些"占位"留在 main 分支上,要么阻塞外部使用者,要么逼他们走 `go install` / clone 路线 —— 而模块路径还没定,任何今天 import 的人都会在重命名时被打断。本 ADR 的目标是把"v0.1.0 出门要做哪些事"一次性钉死,后续 PR 按 D1..D9 机械落地。

## 验证

不展开样本采集,但锚定三条已确证的事实作为决策输入:

1. **目标受众**:用户多轮讨论后明确 v0.1 只服务"公司内部个人开发者" —— 一台笔记本同时跑 server 和 hook,不做多租户。多用户隔离模型尚未成型,强行在 v0.1 预埋反而会让 v0.2 改起来更贵。
2. **README 现状**:占位模块名 + "许可待定"已经是直接证据 —— 任何今天的 `go install` 路径都会在模块重命名时断,任何外部公司的法务都过不了"许可待定"四个字。
3. **CI 现状**:`ci.yml` 没有 release job、没有 GHCR 推送、没有交叉编译矩阵。v0.1 必须把这条流水线补出来,但**只补到目标受众实际需要的程度**,不预先做 Helm / Sigstore / Windows。

落地实施前,需要在一台干净的 macOS arm64 与 Linux amd64 上各跑一次 D8 中描述的 `setup --personal` 流程,把假设(docker 已装、`~/.claude/settings.json` 既有形态)与缺口写进实施 PR。

## 决定

### D1. v0.1.0 只发布 personal mode,team mode 显式延后

v0.1 = 一名开发者在自己机器上同时跑 collector + hook + UI。**不做**多用户隔离、who-sees-what、按用户分段查询。SPEC §13 已经写了 single-node Docker Compose 即 MVP,personal mode 是 single-node 在使用模式上的自然投影,不与 SPEC 冲突。

**否决备选**:在 v0.1 预埋 team-mode hooks(per-user table、auth header 占位、UI 多租户切换)。数据隔离模型(field-level vs row-level vs schema-level、auth 选型、数据迁移策略)需要单独 ADR。预埋会同时绑定四个未决问题,等 v0.2 真要做时全得拆掉重来。v0.2 必须以新 ADR 形式重启该话题。

### D2. 许可证:Apache-2.0

替换 `README.md` 末尾的"许可:待定",新增 `LICENSE` 文件。

选 Apache-2.0 的理由:**有专利授权条款**(MIT 没有,对企业用户有法律意义);足够宽松,公司法务过审时摩擦极小;Go 自托管基础设施生态默认惯例(Kubernetes / Prometheus / Tekton / cosign 都是 Apache-2.0),依赖兼容性零问题。

**否决备选**:MIT 没有专利授权,对一个会签名审计输出的工具来说太薄;AGPL 与"用户自部署、不再向上游回流"的目标受众不匹配,且 SaaS 场景在 SPEC §13 已显式排除,AGPL 的 SaaS 对抗性条款没用武之地。

### D3. 模块路径定为 `github.com/dong-qiu/agent-lens`

`go.mod` 当前的 `github.com/dongqiu/agent-lens` 是占位(README 第 338 行已注明),实际 GitHub 组织是 `dong-qiu`(带连字符)。v0.1.0 之前一次性改完,之后任何外部 import 都按定型路径走。

**否决备选**:保留占位、`go.mod` 与 GitHub 路径不一致。任何外部 `go get` / `go install` 路径会因路径不匹配直接失败,等同于不可用。

### D4. 分发渠道:GitHub Releases + GHCR

- 二进制:`gh release` artifact,跨编译 darwin/linux × amd64/arm64。
- 容器镜像:`ghcr.io/dong-qiu/agent-lens:vX.Y.Z` 与 `:latest`,由 release workflow 推送。
- v0.1 **不做**:内部 Artifactory 镜像、离线 tarball 包、Homebrew tap、apt/yum 仓库。

理由:目标受众是初创团队 / 个人开发者,GHCR pull 完全够用;真正需要内部镜像的客户(气隙、内网受限)等他们出现且明确诉求时再加,SPEC §13 提到的"气隙运行"承诺通过"镜像可被本地 docker save / load"间接满足,不需要 v0.1 出官方离线包。

**否决备选**:Homebrew tap。维护一个 tap 需要 release-please / formula 自动更新,边际收益对 macOS 用户有限,等 v0.2+ 真有用户要再加。

### D5. 平台矩阵:darwin/linux × amd64/arm64

Windows 不在 v0.1 范围。目标受众基本是 macOS / Linux 工程师,Hook 二进制要嵌入 shell-style 的 Claude Code settings,Windows 上 path 处理与 settings 写入路径分支会让 D8 的 `setup --personal` 成本翻倍。等真有 Windows 用户开 issue 再单独加 GOOS=windows 矩阵。

**否决备选**:用 GoReleaser 默认全平台矩阵。多出来的 Windows / FreeBSD 二进制没人测,签名后还得维护,纯负担。

### D6. 发布签名:项目自有 ed25519 (DSSE),不接 Sigstore

复用 `internal/attest` + ed25519 keypair 给 release 二进制做 DSSE 签名 —— 这条决定本身是 dogfood:Agent Lens 这个透明性工具,**用它自己提供给用户的签名原语,签自己的发布**。如果原语不好用,作者第一个被卡住。

私钥放 GitHub Actions secrets;公钥(`agent-lens-public.pem`)随 release 发出,用户验签不需要 trust-on-first-use。

Sigstore (Fulcio + Rekor) 在线签名仍属 ADR 0001 中标记的"可选"路径,留给 v0.2+ 真要走时单独决策。**密钥轮转策略**(轮转触发条件、旧版本 verify 兼容窗口、被泄露场景的撤销路径)在本 ADR **范围之外**,标记为后续工作。

**否决备选**:v0.1 直接上 Sigstore。Fulcio 需要 OIDC 认证 + 网络可达,与 SPEC §13 "气隙运行"目标在产品调性上拧着;且引入会让 release workflow 与外部 CA 的可用性强耦合。

### D7. v0.1.0 release artifact 清单

GitHub Release 必须含:

- `agent-lens-{darwin,linux}-{amd64,arm64}` —— server 二进制
- `agent-lens-hook-{darwin,linux}-{amd64,arm64}` —— hook 二进制
- `checksums.txt` —— 全部二进制的 sha256
- `*.sig` —— 每个二进制的 DSSE 签名
- `agent-lens-public.pem` —— 验签公钥
- 容器镜像:`ghcr.io/dong-qiu/agent-lens:v0.1.0`(同时打 `:latest`)

Release notes 中必须给出"如何用 `agent-lens-hook verify-attestation` 校验下载"的复现命令。

### D8. UX:`agent-lens-hook setup --personal` 取代手改 settings.json

当前 README 让用户手动编辑 `~/.claude/settings.json` 写入 hook 配置。这是已知失败点 —— JSON 语法错误、覆盖既有 hook、不知道路径。v0.1 必须提供子命令封装:

- `agent-lens-hook setup --personal`:
  - 检查 `docker` 可用,否则给清晰提示并退出。
  - 用 GHCR 上发布好的镜像启动 compose stack(**不依赖源码 clone**)。
  - 幂等地把本工具的 hook 注册合并进 `~/.claude/settings.json` —— 必须**保留**用户为其它工具(其它 MCP server / 其它 hook)写的条目,只增不删。
  - 启动后 curl `/healthz` 跑一次 smoke check,失败给可执行的修复提示。
- `agent-lens-hook setup --uninstall`:把上一步加的条目精确移除,留下用户自己的部分,并 `docker compose down -v` 清理。

### D9. 显式不在 v0.1 范围(延后 v0.2+)

为了让未来读者不疑惑"为什么没做",留档:

- Helm chart(`deploy/helm/` 留空)。
- Multi-user / team mode / 数据隔离(需新 ADR)。
- OIDC / SSO / per-user 认证。
- 内部 Artifactory / 离线 tarball 分发。
- Sigstore 在线签名(Fulcio + Rekor)。
- Windows 二进制。
- Homebrew tap。
- `go install` 直接路径:在 D3 模块名定型后**事实上能跑**,但 v0.1 不官方支持,文档不引导,直到 v0.1 正式发布且模块名落定。

## 后果

- README 末尾"许可:待定"与"模块名占位"两条文字消除,被 LICENSE 文件 + go.mod 改动 + ghcr 镜像 URL 替换。
- `.github/workflows/` 增加 `release.yml`,触发条件 `push tag v*.*.*`,产出 D7 清单 + 推 GHCR + 上传签名。
- `script/install-dogfood.sh` 与 D8 的 `setup --personal` 子命令长期共存:前者继续给开发者用(从源码起步 + 热重建),后者给 v0.1 起的所有外部用户用。两条路径在文档中分章节区分。
- v0.2+ team-mode 工作显式落到 D9 列表,不在本 ADR 中预埋接口。其中**多用户隔离模型**必须单独 ADR(field-level vs row-level vs schema-level、auth 选型、迁移策略)。
- 选 Apache-2.0 后,所有外部贡献按 Apache-2.0 接收;CLA / DCO 选择是后续工作,本 ADR 不规定。
- DSSE 私钥放 GitHub Actions secrets:轮转策略、被泄露场景的撤销路径**未决**,标记为后续 ADR。轮转之前的 release 签名永远只能用当时的公钥验,这个约束写入 release notes 模板。
- SPEC 不动:§13 single-node Docker Compose 即 MVP 的措辞与 v0.1 personal mode 完全一致,§14 M3 完成后的"对外发布"不在既有里程碑表格内,本 ADR 自然延伸不冲突。

## 替代方案

- **Helm-first 发布**:让 v0.1 直接出 Helm chart。被否决:目标受众一台笔记本起步,K8s 不在他们的入门路径上;Helm 真正回本是在 HA 部署,与 v0.1 受众不匹配。Helm 留给 v0.2+ 当真有团队部署诉求时再做。
- **OSS 公开发布作为 v0.1 目标**:把"对 Hacker News / Twitter 公布"列入 v0.1 范围。被否决:公开发布需要的不只是二进制,还要文档站、入门教程、社区 issue triage 节奏 —— v0.1 只确认"内部个人开发者能下载装上跑通",对外曝光留给 v0.2 或更晚,等 dogfood 攒够使用证据再说。
- **现在就上 Sigstore**:见 D6 否决理由。气隙调性 + 外部 CA 可用性耦合,v0.1 不值得。

## 后续工作

按 D1..D9 拆分的实施 PR 清单(每条独立可 review):

1. **LICENSE + README**:加 Apache-2.0 LICENSE 文件,改 README 末尾"许可"段,改"模块名"段(D2 / D3)。
2. **模块路径重命名**:`go mod edit -module github.com/dong-qiu/agent-lens` + 全仓 import 路径替换 + CI 验证(D3)。
3. **`.github/workflows/release.yml`**:tag 触发,矩阵交叉编译 D5,产出 D7 清单,DSSE 签名,推 GHCR(D4 / D6 / D7)。
4. **`agent-lens-hook setup --personal` / `--uninstall` 子命令**:实现 + 单元测试 + 在 macOS / Linux 各跑一次手测(D8)。
5. **release notes 模板**:含 `verify-attestation` 复现命令(D7)。
6. **后续 ADR(不在本 ADR 范围)**:DSSE 密钥轮转;v0.2 team-mode 数据隔离模型;Sigstore 接入(if/when);Helm chart 设计。

**本 ADR 不带任何代码改动**。
