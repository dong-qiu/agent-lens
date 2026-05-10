# Agent Lens v0.1.1 — release-engineering patch

v0.1.0 之后第一个 patch。**不是功能版本**——v0.1.1 二进制和 v0.1.0 在功能上等价（源码无变化，仅 CI / 文档调整）。原因是 v0.1.0 cut 过程中暴露了 8 项 release-engineering 问题（见 [#93](https://github.com/dong-qiu/agent-lens/issues/93)），其中影响最直接的两项已合到 v0.1.1。

## 修了什么

- **`checksums.txt` 去重**（v0.1.0 latent bug）：v0.1.0 的 `checksums.txt` 把 `agent-lens-hook-*` 4 个文件每个列了两遍——`sha256sum agent-lens-* agent-lens-hook-*` 两个 glob 互相覆盖。`sha256sum --check` 通过性不受影响，但看着不专业。v0.1.1 改成单 glob `sha256sum agent-lens-*`（已包含 hook 二进制）。
- **CI 端 Dockerfile build 验证**（[#94](https://github.com/dong-qiu/agent-lens/pull/94)）：每个 PR 现在会 build 一次 `Dockerfile.server`（linux/amd64，no push）。v0.1.0 的前 3 次 tag 失败（pnpm safety check / QEMU arm64 timeout）都属于"PR 阶段本可 catch"的 Dockerfile 问题，此后这一类不再带到 tag 之后。
- **release.yml 加 `workflow_dispatch` 触发器**（[#94](https://github.com/dong-qiu/agent-lens/pull/94)）：在 tag 之前可以 `gh workflow run release.yml -f dry_run=true` 干跑整条 pipeline——build + sign 都跑，但 GHCR 不 push、GitHub Release 不创。**v0.1.1 自身就是用这个机制 pre-tag 验过的**——dry-run 在 PR 分支上立刻 catch 到一个潜伏的 image-tag bug（branch 名含 `/`，OCI 拒），合 PR 前修了。

## 没动什么

- **二进制内容**：`-trimpath -ldflags="-s -w"` 让 Go 在源码相同时产出字节相等的二进制，所以 v0.1.1 的二进制 sha256 应等于 v0.1.0 的。`.sig` 文件因为 `BuiltAt` 时间戳不同会变。
- **API / event schema / GraphQL**：零变化。
- **docs**：除了 `RELEASE_NOTES_v0.1.1.md` 自身、README 把 download URL 改成 `releases/latest/download/`（消除未来 patch 的 README churn）外，无变化。

## 升级

```bash
# 装新二进制（README 现在用 /latest/ 自动指向 v0.1.1）
curl -fsSL https://github.com/dong-qiu/agent-lens/releases/latest/download/agent-lens-hook-darwin-arm64 \
  -o /usr/local/bin/agent-lens-hook
chmod +x /usr/local/bin/agent-lens-hook

# 容器：:latest 自动指 v0.1.1，或显式
docker pull ghcr.io/dong-qiu/agent-lens:v0.1.1
```

如果你已经在跑 v0.1.0，**没必要升**——除非你在 verify checksums.txt 的时候被重复行困扰，或者要从 GHCR `:latest` 拉镜像。

## 还没修的（#93 余下的 6 项）

| # | 项 | 状态 |
|---|---|---|
| 3 | ADR 0009 容器平台范围 | v0.2 |
| 4 | 原生 arm64 runner OR split web-build | v0.2 |
| 5 | RC tag pattern (`v*-rc.*`) | 待开 |
| 6 | release file `/review` 政策 | 待开 |
| 7 | release file 强制 `/review` | 待开 |
| 8 | `/self-review` skill 更新（吸收 v0.1.0 教训） | 待开 |

最关键的"PR 阶段 catch Dockerfile 类问题"已经在 v0.1.1 里覆盖；剩下 6 项是流程纪律 + 多架构成本优化，不阻塞用户使用。
