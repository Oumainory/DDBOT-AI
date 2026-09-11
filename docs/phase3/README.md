# DDBOT-AI Phase 3 — Domain / Projection Foundation

状态：**PHASE 3 IMPLEMENTATION DONE / READY FOR FREEZE；AI NOT STARTED**。

Phase 2 已在 `phase2-baseline`（`998388b529b9f9dc472ba29e20bd29a5f63270c1`）冻结。Phase 3A 从该点开始增加 Source、Target、Connector 元数据和 Dashboard 管理，但不把 SQLite 变成 Legacy 订阅真相源。

## 范围

- BuntDB 继续是 Legacy Subscription 的唯一权威源；聊天命令和 Dashboard 通过同一个 Legacy subscription service 修改它。
- SQLite v7 只保存可重建的 Source、Target、Connector 和 `subscription_projections` 元数据。
- Projection 从 BuntDB snapshot 事务性重建；漂移时重建，重建失败不会覆盖 BuntDB。
- Source 支持 Bilibili UID/官方 profile URL、Twitter/X exact handle/profile URL；没有通用 URL 抓取。
- Target 仅支持 group/channel，唯一键为 `(connector_id, target_type, external_id)`，不支持私聊用户。
- Connector 允许至多一个启用的 main，Telegram 只能作为 extra；credential 只保存 Secret Store reference。
- Domain mutation 使用认证、Origin、CSRF 和 Idempotency-Key；connector test 是认证的、Origin/CSRF 保护且有速率限制的安全 POST，不需要幂等键。

Phase 3B 增加 Connector Migration、`migration_held` durable hold、Telegram pairing、Audit 和 Dashboard Migration Wizard；AI Provider、Shadow、Profile 和 ENFORCE 仍未开始。详见 [P3B Connector Migration](./P3B_CONNECTOR_MIGRATION.md)。
