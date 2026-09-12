# SQLite 所有权边界

Phase 0 只固定最容易被错误实现的持久化契约。Core 是 SQLite 的唯一 writer；Dashboard、聊天命令和未来 WebUI 都通过同一个 Service/API 修改状态，不直接打开数据库文件。

权威 schema source 是 `internal/platformdb/migrations/*.sql`，而不是本文件或下面的参考
SQL。已发布的 migration（尤其 `001_core.sql`）是 immutable；后续修正必须新增有序、独立
checksum 的 migration。

当前 v10 schema 固定 Phase 4 AI Shadow 平台状态：

- `idempotency_records` 保存主体、Key、uppercase method、concrete path、canonical query、body SHA-256、command type、显式 execution status、sanitized response、创建/完成/过期时间；原始敏感 request body 永不落库；
- `delivery_migration_holds` 保存 `migration_held` 重启所需的 delivery/event、独立 route decision identity、route snapshot、logical target 和 message snapshot。
- `administrators`、`setup_state`、`setup_tokens` 和 `sessions` 保存 P1B 的唯一管理员、一次性
  bootstrap token 与 server-side Session；原始 Setup/Session token 永不落库。
- `secret_store_state`、`credentials` 和 `credential_secrets` 保存 P1C 的加密哨兵、非敏感
  credential metadata 与 AES-256-GCM envelope；Master Key 和 plaintext 永不进入 SQLite。
- `observed_events`、`route_observations` 和 `delivery_observations` 保存 P2A 的旁路观察事实；
  只写固定 allowlist 的 public snapshot、路由结果和真实 Messenger 结果，不参与 Legacy
  过滤或投递决定，也不是通用重试队列。
- `sources`、`connectors`、`targets` 和 `subscription_projections` 保存 P3A 可重建的
  Domain 元数据；BuntDB 仍是 Legacy Subscription 唯一权威源。P3A 的完整约束见
  [`docs/phase3/P3A_DOMAIN_PROJECTION_DISCOVERY.md`](../phase3/P3A_DOMAIN_PROJECTION_DISCOVERY.md)。
- `connector_migrations` 与 `connector_migration_mappings` 保存显式 Target 映射和可恢复的
  migration journal；`delivery_migration_holds` 另有 release claim marker 与完整 payload；
  `telegram_pairing_challenges` 只保存短期 code hash；`audit_entries` 是应用级 append-only
  hash chain。Audit retention 只通过统一 prune 操作完成，并写入 `audit.prune` anchor；
  它们都不保存明文 secret，也不是通用 delivery retry queue。
- v9 仅扩展旁路 `delivery_observations.status`，允许受控迁移期间的
  `migration_held` 结果；它不改变 Legacy 投递或建立通用重试队列。
- v10 增加 AI Shadow 的 provider 元数据、ClassifierRelease、NormalizedEvent、
  event-level decision、route evaluation、结构化 Profile/Policy 以及显式 Evaluation
  case/run/result。API key、prompt、原始 provider response 和完整 prompt 均不落库；
  `effective_action` 在 Phase 4 强制为 `pass`。NormalizedEvent、AI decision 与 route
  evaluation 遵守 90 天旁路保留边界，Evaluation dataset 仅显式删除。

Phase 4 的权威 schema 仍是 `internal/platformdb/migrations/010_ai_shadow.sql`，本文件和
`SQLITE_FOUNDATION.sql` 只是非权威参考；旧 migration 001–009 不得修改。

P1C 的 Master Key 文件格式、AAD、Recovery 与 health/readiness 契约见
[`docs/phase1/P1C_SECRET_STORE.md`](../phase1/P1C_SECRET_STORE.md)。

旧 v1 幂等行升级时，`command_type` 为 `unknown`，`execution_status` 从旧 `status_code`
推导，无法可靠恢复的 `completed_at` 保持 `NULL`。旧 hold 行的 `route_decision_id` 可以
暂时为 `NULL`，不会用猜测值填充；新的持久化 hold 必须提供真实 route identity；v3 的
SQLite trigger 会在数据库层拒绝新增或主动清空 route identity 的写入，而不会阻塞只更新
其它字段的旧 NULL 行。

上线 SQLite adapter 时必须在每个连接上设置 `foreign_keys=ON`，使用 WAL 和 busy timeout，并按 `expires_at` 清理幂等记录和过期 Session。SQLite 文件只能放在本机持久卷，不能由多个进程各自写入，也不能依赖网络文件系统的锁语义。
