# P1A — SQLite Foundation

P1A 是 Phase 1 的第一个实施切片。它只提供 Platform Foundation 的 SQLite owner、migration、backup 和 health/readiness 基础，不把 SQLite 接入 Legacy Subscription，也不改变 WSa 的启动、采集、BuntDB 或推送路径。

## Owner 与打开策略

`internal/platformdb` 不在 `init()` 中打开数据库，也不创建全局句柄。调用方必须显式调用 `platformdb.Open(ctx, platformdb.Config{...})` 并持有返回的 `Store`。

- 每个进程只有一个显式 SQLite owner；`database/sql` 连接池固定为一个连接。
- 数据库父目录由 owner 创建，文件权限尽量收紧为 `0600`，目录权限尽量收紧为 `0700`。
- 原生运行时的数据库路径由上层配置决定；测试可以使用 `:memory:`，文件数据库使用 WAL。
- 打开、配置或 migration 失败只返回 error，不 panic、不启动 goroutine，也不修改 Legacy 状态。
- 主程序使用纯 Go 的 `modernc.org/sqlite` 驱动，保持 `CGO_ENABLED=0` 发行门禁。

每个 owner 在 migration 前设置并验证：

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
```

`foreign_keys` 和 `busy_timeout` 会在连接上读回确认；文件数据库必须确认最终 journal mode 为 `wal`。

## Migration 与版本表

内置 migration 通过 `go:embed` 随二进制发布，不依赖当前工作目录。`schema_migrations` 保存：

```text
version
name
checksum = sha256:<embedded SQL>
applied_at
```

每个 migration 在事务内执行。已经应用的版本必须同时匹配 name 和 checksum；修改已发布 SQL、未知版本或高于当前二进制的版本都会拒绝启动，避免静默降级或重写既有数据。当前 P1A migration 版本为 `1/core`，包含 Phase 0 已冻结的幂等记录表和 `migration_held` durable hold 表。

## 备份

`Store.Backup(ctx, destination)` 使用 SQLite `VACUUM INTO` 生成一致性快照：

- 自动创建目标父目录；
- 目标文件已存在时拒绝覆盖；
- 源路径与目标路径相同时拒绝执行；
- 生成的目标文件尽量设置为 `0600`；
- 备份本身不是通用重试队列，也不改变 Legacy 投递状态。

## Health 与 readiness

`platformdb.NewProbe(store, openErr)` 为上层 API 提供两个可直接挂载的 handler：

```text
Probe.Healthz()  → GET /healthz
Probe.Readyz()   → GET /readyz
```

语义固定为：

- `/healthz` 表示进程仍然存活。SQLite 初始化失败时报告 `degraded`，但仍返回 HTTP 200，避免把 Legacy Core 判死。
- `/readyz` 表示 Platform Foundation 是否具备提供 Dashboard/API 的依赖。SQLite 不可用、schema 不完整或版本不匹配时报告 `not_ready`，返回 HTTP 503。
- 健康响应只返回稳定的状态和非敏感 code，不返回数据库路径、原始 error、Secret、Session 或配置明文。

上层启动路径应记录 `Open` 的详细 error，同时将 `(nil, err)` 交给 `NewProbe`，继续 Legacy Core 的独立启动；只有后续功能请求依赖 readiness 时才拒绝该新功能。

## P1A 验证

当前实现的测试覆盖：

- WAL、foreign keys、busy timeout 和 migration version；
- 重启后的幂等 migration；
- future schema 拒绝与不降级；
- 一致性 backup、目标覆盖保护和 source/destination 冲突；
- SQLite 打开失败时 health 仍存活、readiness 返回 503；
- health/readiness HTTP method、状态码和敏感信息边界。

本切片仍然不接入 Admin Auth、Secret Store、`/api/v2` 业务命令、Vue Shell、AI、Connector 或 SQLite Subscription Primary。
