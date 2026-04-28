# ADR 0001:v1 技术栈

- 状态:Accepted
- 日期:2026-04-27
- 取代:—

## 背景

Agent Lens 起步于全新仓库。在 M1 脚手架开建之前,需要先把语言、持久化、wire format、前端栈这几条主轴定下来,后续 ADR 才不会反复推翻。

不可妥协的输入条件:

- 自托管优先,必须能在断网环境运行。
- 阶段边界的 attestation 必须符合 in-toto / SLSA(`SPEC.md` §11)。
- append-only 事件日志 + hash chain。
- Hook 作者从 Bash + curl 到完整 Go 服务都有,ingest 必须两类都能接。

## 决定

- 后端用 **Go**(chi + pgx + sqlc + golang-migrate)。
- 规范 schema 用 **Protobuf**,由 `buf` 管理。Ingest 路径 v1 走 **HTTP + NDJSON**;gRPC 后续可加,不需要 schema 改动。
- 事件存储:**Postgres**;对象存储:**MinIO**(S3 协议)。
- 签名:`sigstore-go` + 本地 ed25519 密钥默认开;Fulcio / Rekor 走可选。
- 前端:**React + TS + Vite + Tailwind + shadcn/ui**,因果图用 **ReactFlow**,diff / code 视图用 **Monaco**。
- Hook 二进制:单个 Go binary,带子命令(`claude` / `git-post-commit` / `verify` / `export`)。

## 评估过的备选

- **Rust 后端**。技术上可行,但失去供应链生态优势(in-toto-golang、sigstore-go、cosign、slsa-verifier 都是 Go),MVP 阶段会拖慢迭代。重新考虑的条件:(a) 团队已经深度 Rust 熟练;(b) ingest 压力超过 Go 余量;(c) 需要把 ingest 内核做成可嵌入库。
- **gRPC 用作 ingest**。真正的收益(二进制、双向流)在我们这种事件量下不回本;可调试性和 `curl`-friendly 才回本。Schema 是共用的,所以这是延后,不是否决。
- **ClickHouse 作为权威存储**。分析能力强,但排序语义会让 hash chain 复杂化。后续作为 CDC-fed 副本接入,服务于分析查询。
- **Svelte 前端**。更轻更快,但 ReactFlow + Monaco 是决定性组件,它们的 React 生态更厚。

## 后果

- 仓库需要同时维护 Go 和 Node/TS 两套工具链。Hook 作者只需要 Go(或一个会调 curl 的 shell)。
- Postgres 成为完整性关键组件:M3 之前必须写出备份与恢复流程。
- 选 Go 把 Agent Lens 的发布节奏松绑到 Sigstore / cosign 的发布节奏上。我们在 `go.mod` 里 pin 版本。
- gRPC ingest、ClickHouse 分析副本、Sigstore 网络签名都是显式的后续项,不是承诺 —— 每条要做时单开 ADR。
