# SQLite 所有权边界

Phase 0 只固定最容易被错误实现的持久化契约。Core 是 SQLite 的唯一 writer；Dashboard、聊天命令和未来 WebUI 都通过同一个 Service/API 修改状态，不直接打开数据库文件。

权威 schema source 是 `internal/platformdb/migrations/*.sql`，而不是本文件或下面的参考
SQL。已发布的 migration（尤其 `001_core.sql`）是 immutable；后续修正必须新增有序、独立
checksum 的 migration。

当前 v4 schema 固定三类平台状态：

- `idempotency_records` 保存主体、Key、uppercase method、concrete path、canonical query、body SHA-256、command type、显式 execution status、sanitized response、创建/完成/过期时间；原始敏感 request body 永不落库；
- `delivery_migration_holds` 保存 `migration_held` 重启所需的 delivery/event、独立 route decision identity、route snapshot、logical target 和 message snapshot。
- `administrators`、`setup_state`、`setup_tokens` 和 `sessions` 保存 P1B 的唯一管理员、一次性
  bootstrap token 与 server-side Session；原始 Setup/Session token 永不落库。

旧 v1 幂等行升级时，`command_type` 为 `unknown`，`execution_status` 从旧 `status_code`
推导，无法可靠恢复的 `completed_at` 保持 `NULL`。旧 hold 行的 `route_decision_id` 可以
暂时为 `NULL`，不会用猜测值填充；新的持久化 hold 必须提供真实 route identity；v3 的
SQLite trigger 会在数据库层拒绝新增或主动清空 route identity 的写入，而不会阻塞只更新
其它字段的旧 NULL 行。

上线 SQLite adapter 时必须在每个连接上设置 `foreign_keys=ON`，使用 WAL 和 busy timeout，并按 `expires_at` 清理幂等记录和过期 Session。SQLite 文件只能放在本机持久卷，不能由多个进程各自写入，也不能依赖网络文件系统的锁语义。
