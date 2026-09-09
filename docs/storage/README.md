# SQLite 所有权边界

Phase 0 只固定最容易被错误实现的持久化契约。Core 是 SQLite 的唯一 writer；Dashboard、聊天命令和未来 WebUI 都通过同一个 Service/API 修改状态，不直接打开数据库文件。

`SQLITE_FOUNDATION.sql` 固定两类行：

- `idempotency_records` 保存主体、Key、方法、规范化路径、body hash、原始响应和 7 天过期时间；
- `delivery_migration_holds` 保存 `migration_held` 重启所需的 event、route snapshot、logical target 和 message snapshot。

上线 SQLite adapter 时必须在每个连接上设置 `foreign_keys=ON`，使用 WAL 和 busy timeout，并按 `expires_at` 清理幂等记录。SQLite 文件只能放在本机持久卷，不能由多个进程各自写入，也不能依赖网络文件系统的锁语义。
