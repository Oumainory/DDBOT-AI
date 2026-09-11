# DDBOT-AI Phase 2 — Observation / Routing Foundation

状态：**P2A IMPLEMENTING — Passive Observation Runtime**。

Phase 1 已在 `phase1-baseline`（`6b1d591f388f50a31855a8f93a50c4a344e2b01e`）冻结。
Phase 2 从该提交开始，只增加旁路可观测性，不改变 Legacy WSa 的订阅、过滤、模板、媒体或投递决定。

## P2A 范围

P2A 只记录已经发生的事实：

```text
Legacy Event
    ↓
ObservedEvent
    ↓
RouteObservation
    ↓
DeliveryObservation
```

Recorder 是 bounded、single-worker、best-effort 的旁路队列。队列满、SQLite 不可用、持久化失败或 worker panic 都只会丢弃 Observation 并计数/记录脱敏诊断；Legacy 调用方不会等待 SQLite，也不会因为 Observation 失败而改变原有行为。

P2A 不实现：

- Source/Target/Subscription/Connector 管理；
- Normalizer、AI Provider、Classifier、Shadow、Profile、ENFORCE；
- Delivery retry、replay、feedback 或 Migration Coordinator；
- Dashboard 查询或新的 Domain API；
- SQLite 成为 Legacy Subscription 的 Primary。

## Durable schema

`internal/platformdb/migrations/006_observation.sql` 是 v6 的 authoritative migration。001–005 保持 immutable。`observed_events`、`route_observations`、`delivery_observations` 只保存 allowlisted public snapshot 和路由/投递结果；不保存 raw response、headers、cookie、token、credential 或 renderer/template 对象。

v5 → v6 仍遵守既有 pre-migration backup gate：先完成 inspection 和 backup，再执行 006；backup 失败时 live schema 保持 v5。Retention 默认 90 天，由显式调用的 bounded janitor 逐批删除 root event，外键级联删除 route/delivery facts。

## Hooks

- Source hook 位于稳定 Event 进入 per-target routing/filter 之前；
- Route hook 位于 deterministic routing/filter 结果之后；
- Delivery hook 位于真实 Messenger 结果之后；Telegram 等没有明确结果的路径记录 `unknown`；
- 全局桥默认 Noop，只有 platform owner 显式安装 Store-backed Recorder 时才写 SQLite。

详见 [P2A Passive Observation](./P2A_PASSIVE_OBSERVATION.md)。
