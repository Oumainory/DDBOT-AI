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

内置 migration 通过 `go:embed` 随二进制发布，不依赖当前工作目录。运行时按文件名
`NNN_name.sql` 解析并按数字版本排序；版本必须从 1 连续、唯一，每个文件都有独立的
`sha256:<embedded SQL>` checksum。`schema_migrations` 保存：

```text
version
name
checksum = sha256:<embedded SQL>
applied_at
```

每个 migration 在一个事务内执行。已经应用的版本必须同时匹配 name 和 checksum；修改已发布 SQL、未知版本、缺失历史或高于当前二进制的版本都会在任何 schema mutation 前拒绝启动，避免静默降级或重写既有数据。

`001_core.sql` 已发布且 immutable，不能被重写或重新计算 checksum。P1A.1 新增
`002_contracts.sql`，P1A.2 新增 `003_contract_guards.sql`；因此 fresh database 按
`1 → 2 → 3` 创建 latest schema，已有 v1/v2 database 按缺失的有序 migration 升级。

## Pre-migration backup gate

打开已有 database 时，owner 严格执行下面的顺序：

```text
open/configure SQLite
    ↓
read-only inspect schema_migrations and validate every applied checksum
    ↓
if existing database has pending migration: create a non-overwriting VACUUM INTO backup
    ↓
confirm backup succeeded
    ↓
apply only pending migrations in one transaction
    ↓
return Store
```

fresh empty database 不需要无意义的备份；已经是 latest schema 的 database 也不备份。
备份目标可以由 `Config.PreMigrationBackup.Destination` 明确指定；为空时由 source path、
版本和 `Config.Now` 生成稳定的、可审计的默认文件名。目标已存在、目标等于 source、
目标不可创建或 `VACUUM INTO` 失败，都会返回 `ErrPreMigrationBackup`，且不会执行任何
pending migration。备份失败时 live schema、原始数据和 `schema_migrations` 都保持不变。

成功的 v1 → v3 gate 因此得到一个仍为 v1 的快照；v2 → v3 同理得到一个仍为 v2 的
快照。只有 live database 在备份成功后才记录对应 migration。后续重新打开 latest v3
不会再次产生 pre-migration backup。

## P1A.1 / P1A.2 schema closure

`002_contracts.sql` 增加：

- `idempotency_records.canonical_query`：规范化 query 的独立审计字段；
- `idempotency_records.command_type`：domain command 名称，历史 v1 行回填为 `unknown`；
- `idempotency_records.execution_status`：显式 `in_progress` / `completed` 状态；历史行由
  v1 的 `status_code = 0` 推导；
- `idempotency_records.completed_at`：完成时间，历史行因无法可靠重建而保持 `NULL`；
- `delivery_migration_holds.route_decision_id`：独立 route identity。旧开发行可以为
  `NULL`，不会伪造身份；新的 hold 必须从 durable payload 提供它。

请求指纹由 uppercase method、normalized concrete path、sorted/canonical query 和
canonical JSON body 的 SHA-256 组成。SQLite 只保存 query、method/path 和 body hash，
不保存原始敏感 request body；cached response 必须是已清洗的响应。第一次 command claim
固定 7 天 `expires_at` 并持久化为 `in_progress`；相同 key/fingerprint 在进行中返回
`idempotency_in_progress`，完成后保存 `completed_at` 和响应，replay 不延长过期时间。

路径与 query 严格分离：`NormalizedPath` 只包含要求以 `/` 开头的 concrete path，经过
`path.Clean` 并去掉根路径之外的尾部 `/`；它永远不包含 `?`。`CanonicalQuery` 只包含
`url.Values.Encode()` 产生的稳定排序、转义 query（无 query 时为空字符串），因此相同
path 的不同 query 必然是不同请求指纹。

P1A.2 的 `003_contract_guards.sql` 在 `delivery_migration_holds` 上增加数据库级双重
保护：新的 INSERT 必须提供非空、非空白 `route_decision_id`，显式把它更新为 NULL/空值
也会被拒绝；应用层 `HeldDelivery.Decode` 与 payload identity 校验仍然保留。升级时不
回填历史身份，v1/v2 已有的 NULL hold 行原样保留；只更新这些旧行的其它字段不会被
`UPDATE OF route_decision_id` trigger 阻塞。

## 备份

`Store.Backup(ctx, destination)` 使用 SQLite `VACUUM INTO` 生成一致性快照；它同时是
pre-migration gate 的底层动作：

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

- WAL、foreign keys、busy timeout 和 ordered migration version 1 → 2 → 3；
- 001/002/003 immutable checksum、future/unknown/missing history 拒绝；
- fresh/latest 不备份、v1 → v3 与 v2 → v3 的真实 migration 前快照、默认/显式目标和冲突保护；
- backup failure 阻止所有 schema mutation，002 失败时事务整体回滚；
- 重启后的幂等 migration，且 latest v3 database 不重复创建 pre-migration backup；
- v3 route decision INSERT/UPDATE trigger、legacy NULL hold 保留和非相关字段更新；
- request fingerprint 的 method/path/query/body 语义、显式 in-progress/completed、completed_at 和固定 7 天 expiry；
- `migration_held` 的 route decision identity 序列化、旧数据安全回填和 crash-style 独立恢复；
- future schema 拒绝与不降级；
- 一致性 backup、目标覆盖保护和 source/destination 冲突；
- SQLite 打开失败时 health 仍存活、readiness 返回 503；
- health/readiness HTTP method、状态码和敏感信息边界。

本切片仍然不接入 Admin Auth、Secret Store、`/api/v2` 业务命令、Vue Shell、AI、Connector 或 SQLite Subscription Primary。
