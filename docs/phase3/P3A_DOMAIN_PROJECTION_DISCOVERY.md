# P3A — Domain, Projection and Discovery

## Durable boundary

`internal/platformdb/migrations/007_domain_core.sql` 是 v7 authoritative migration；001–006 immutable。它建立：

- `sources`：平台和外部身份的唯一键 `(platform, external_id)`；
- `connectors`：OneBot/Satori main 与 Telegram extra 的拓扑元数据；启用 main 有唯一 partial index；
- `targets`：群/频道目标，唯一键 `(connector_id, target_type, external_id)`；
- `subscription_projections`：从 BuntDB 重建的 Legacy 关系和 options snapshot。

所有 domain IDs 由 UUIDv7 生成。`credential_id` 只是 Secret Store 引用，API 不读回 credential 明文，也不接受 connector config 中的 token/password/secret 字段。

## Legacy projection

Legacy Concern 状态仍由 BuntDB 保存。Domain command 的顺序固定为：

```text
authenticated command
  → shared Legacy subscription service
  → BuntDB mutation
  → BuntDB read-back snapshot
  → SQLite projection rebuild
```

启动时和 `GET /api/v2/subscriptions` 读取前会进行 best-effort snapshot/reconciliation。SQLite 失败只返回 degraded/domain-unavailable，不阻止 Legacy 采集和推送。Projection rebuild 在 SQLite 事务内清空并写入完整 snapshot；它不会写回或修复 BuntDB。

## Discovery safety

Discovery 只接受 Bilibili 官方 host 的数字 UID/profile URL，以及 Twitter/X 官方 host 的 exact handle/profile URL。不会对用户提供的任意 URL 发请求。Bilibili search 是可注入的 resolver capability；没有真实 resolver 时返回稳定 `discovery_unavailable`，direct resolve 仍可用。

Satori 频道需要显式 channel ID，或在同一 guild 中恰好只有一个可发送 text channel；多候选时返回 `ambiguous_target`，不会猜测。

## API boundary

只读：

```text
GET /api/v2/sources
GET /api/v2/sources/{id}
GET /api/v2/sources/{id}/targets
GET /api/v2/targets
GET /api/v2/targets/{id}
GET /api/v2/targets/{id}/sources
GET /api/v2/connectors
GET /api/v2/connectors/{id}
GET /api/v2/subscriptions
```

Domain commands：

```text
POST/PATCH/DELETE /api/v2/sources[/{id}]
POST/PATCH/DELETE /api/v2/targets[/{id}]
PATCH /api/v2/connectors/{id}
POST /api/v2/connectors/{id}/test
POST/PATCH/DELETE /api/v2/subscriptions[/{id}]
POST /api/v2/subscriptions/rebuild-projection
```

除 connector test 外，命令必须携带 `Idempotency-Key`；请求指纹沿用 `internal/idempotency` 的 uppercase method、concrete normalized path、canonical sorted query 和 canonical JSON body SHA-256。相同 key/fingerprint replay 原响应，不同语义返回 `idempotency_conflict`，进行中的请求返回 `idempotency_in_progress`。

## Dashboard

Phase 3A Dashboard 增加 Sources、Targets、Connectors 页面，并保留 Overview、Observations、About。页面只显示 credential configured/masked 状态和脱敏 config，不提供 raw YAML/editor、Secret reveal、Replay 或 AI 设置。
