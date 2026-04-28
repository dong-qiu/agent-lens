# Postgres + MinIO 备份与恢复 runbook

满足 ADR 0001 收尾时的承诺:Postgres 是完整性关键组件,M3 之前必须落地备份与恢复流程。本文档面向运维者,**不是**架构设计文档。

## 1. 备份对象与威胁模型

| 资产 | 内容 | 丢失后果 |
|---|---|---|
| `events` 表 | append-only 事件流 + hash chain | **审计链断裂,无法重建** |
| `links` 表 | linker worker 推断的关系 | 可重新派生,但需要重跑 |
| `artifacts` 表 | 指向 MinIO blob 的引用元数据 | 与 MinIO 数据成对丢失 |
| MinIO bucket | 大块 artifact(完整 transcript / diff bundle 等) | 引用悬空 |
| `schema_migrations` 表 | 迁移版本号 | 不知道当前 schema 状态 |

**目标**:任何单台磁盘损坏 / 容器损坏 / 误删除场景下都能在 < 30 分钟内重建到最近一次备份的状态,**且 hash chain 通过 `agent-lens-hook verify` 验证**。

不在范围内:多区域 DR、加密 off-site 自动上传、PITR(point-in-time recovery)。这些是运维方按需在本 runbook 之上叠加的。

## 2. 工具

- `scripts/pg-backup.sh` —— 调 `pg_dump --format=custom`,产出 `agentlens-<ts>.dump` + `.sha256` sidecar
- `scripts/pg-restore.sh` —— 调 `pg_restore`,带 sha256 校验和"非空目标库拒绝覆盖"的安全闸
- `scripts/verify-backup-integrity.sh` —— 完整 round-trip:dump → 还原到临时库 → 启临时 collector → 跑 `agent-lens-hook verify` —— **这是验证备份真有用的唯一可信路径**,光备份成功不够
- Make targets:`make db-backup` / `make db-restore` / `make db-verify-backup`

依赖:`pg_dump` / `pg_restore` / `psql`(`brew install postgresql-client` 或容器里自带)。

## 3. 日常备份(MVP 推荐节奏)

**单机 dogfood / 小规模自托管**:每日一次。

```bash
# 默认输出到 ./backups/
make db-backup

# 或者指定输出目录
BACKUP_DIR=/path/to/backups scripts/pg-backup.sh
```

产物:`agentlens-YYYYMMDDHHMMSS Z.dump`(custom format,gzip 9 级压缩)+ 同名 `.sha256`。

**保留策略**(本 runbook 不强制,运维方按需):
- 最近 7 份每日备份
- 最近 4 份每周备份(每周日)
- 最近 12 份每月备份(每月 1 日)

老备份手动删 / cron rotate / `find -mtime +N -delete`,本仓库不内置。

**外部存储**:本仓库 _不_ 推送备份到任何云端。若需要,在 `make db-backup` 后追加运维侧的 `rclone` / `aws s3 cp` / `mc cp` —— 加密责任也在那一步,不在 `pg-backup.sh` 里。

## 4. 恢复

### 4.1 整库恢复到默认 DSN

```bash
# 默认会跑 pg-restore,目标 = PG_DSN(默认开发 DSN)
make db-restore DUMP=backups/agentlens-20260428-120000Z.dump
```

`pg-restore.sh` 默认**拒绝**写入非空数据库。如果确实是要覆盖现有内容(例如紧急回滚):

```bash
TARGET_OVERWRITE=1 scripts/pg-restore.sh backups/agentlens-...dump
```

### 4.2 恢复到不同的目标库

```bash
scripts/pg-restore.sh backups/agentlens-...dump \
  postgres://user:pass@otherhost:5432/agentlens?sslmode=disable
```

### 4.3 恢复后必做的完整性校验

恢复 ≠ 数据可信。**必须**针对至少一个已知 session id 跑一遍 `agent-lens-hook verify`,确认 hash chain 没断。

```bash
# 列出库里的 session id
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  psql -U agentlens -d agentlens -c \
  "SELECT session_id, COUNT(*) FROM events GROUP BY session_id ORDER BY MAX(ts) DESC LIMIT 5"

# 校验最大的那个
agent-lens-hook verify --session <session_id> --quiet
```

如果输出 `OK · N events · head <hash>`,恢复有效。任何 `FAIL` 都意味着备份本身或恢复路径有问题(参见第 6 节 Troubleshooting)。

## 5. 备份本身是否可用?

> 没验过的备份 = 没备份。

`scripts/verify-backup-integrity.sh` 做完整 round-trip,**完全不动 production 数据库**:

```bash
# 找一个真实 session id 来跑
make db-verify-backup SESSION=c3e32521-b0c5-49b0-a218-d7995acb9acd
```

它会:
1. 用 `pg-backup.sh` 当场抓一份新 dump(临时,自动清理)
2. 在同一 PG 实例上 `CREATE DATABASE agentlens_verify_<pid>` 临时库
3. `pg-restore.sh` 还原进临时库
4. `go run ./cmd/agent-lens` 起一个临时 collector(`:18787`),指向临时库
5. 跑 `agent-lens-hook verify --session <id> --url http://localhost:18787`
6. 干掉临时 collector + `DROP DATABASE` 临时库

**绿 = 备份链路真的可用**。**红 = 修(或丢弃这次备份)**。

建议每周跑一次。Cron 例:

```cron
# 周日凌晨 3 点跑 round-trip 验证
0 3 * * 0 cd /opt/agent-lens && make db-verify-backup SESSION=<sentinel-session-id> >> /var/log/agentlens-backup-verify.log 2>&1
```

挑一个不会被删的 sentinel session 作为常驻验证靶子。

## 6. MinIO artifact bucket 备份

artifacts 表只存对 MinIO 对象的引用;**真实 blob 在 MinIO 那边**,需要单独备份。

```bash
# 装 mc(MinIO client):brew install minio/stable/mc
mc alias set local http://localhost:9000 agentlens agentlens-secret

# 把整个 bucket 镜像到本地目录
mc mirror local/agent-lens ./backups/minio-$(date -u +%Y%m%d-%H%M%SZ)/

# 恢复
mc mirror ./backups/minio-... local/agent-lens
```

dogfood 当前 artifacts 表为空(M3 attestation 写 MinIO 路径还没点亮),所以日常先跳过 MinIO 备份。**真有 artifact 入库时**(可以用 `SELECT COUNT(*) FROM artifacts` 监控),把 `mc mirror` 同步进 `make db-backup` 同一节奏。

## 7. Troubleshooting

| 现象 | 排查 |
|---|---|
| `pg_dump: error: server version mismatch` | 安装与服务端同主版本的 `postgresql-client`,或者用 `docker compose exec postgres pg_dump` 在容器内跑 |
| `pg-restore.sh` 报 "target already contains N rows" | 默认安全闸。确认要覆盖 → `TARGET_OVERWRITE=1`;要并存 → 恢复到一个新建的临时库 |
| `agent-lens-hook verify` 报 `FAIL at index N` | 先确认 dump 文件 sha256 一致;再用 `verify-backup-integrity.sh` 排除"备份本身坏了"。如果新 dump 也 fail,则恢复路径正常 —— 是源库本身链断了(对比 ADR 0001 / 历史 issue #38 那类 store read-order 问题) |
| 临时库创建失败 (`permission denied to create database`) | 备份用户没有 `CREATEDB` 权限。要么提权,要么手动建库后用 `--target` 指向它 |

## 8. 何时回头看本 runbook

- v1 `agent-lens` 单机 docker compose:本文档够用
- 升 K8s + Helm(SPEC §13 HA 部署):**不够** —— 写新的 runbook,涵盖 StatefulSet 的 PV snapshot、跨可用区复制
- 加 ClickHouse 副本(ADR 0001 后续项):**不够** —— ClickHouse 备份语义不同,单写
- 接 Sigstore 网络签名(ADR 0001 后续项):**不影响** —— 签名材料在事件 payload 内,跟着 events 表一起备份就行
