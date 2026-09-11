# DDBOT-AI Phase 2 — Observation / Routing Foundation

状态：**PHASE 2 DONE / FROZEN**。冻结点为 `phase2-baseline`（`998388b529b9f9dc472ba29e20bd29a5f63270c1`）。

Phase 1 已在 `phase1-baseline`（`6b1d591f388f50a31855a8f93a50c4a344e2b01e`）冻结。
Phase 2 从该提交开始，只增加旁路可观测性，不改变 Legacy WSa 的订阅、过滤、模板、媒体或投递决定。

## P2A 写入范围

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
- SQLite 成为 Legacy Subscription 的 Primary。

## Phase 2 读路径与诊断

Phase 2 在不改变 P2A 写入路径的前提下增加了只读诊断：

- `GET /api/v2/observations/events` 支持 `platform`、`event_type`、`source_kind`、`source_external_id`、RFC3339 `from`/`to` 和稳定 cursor；默认 50、上限 200；
- `GET /api/v2/observations/events/{id}` 聚合 allowlisted public snapshot、Route 和 Delivery；routes/deliveries 也提供独立只读子资源；
- `GET /api/v2/observations/summary` 区分进程运行计数和 SQLite 最近 24 小时事实；
- 所有观察 API 只接受已认证 GET，不提供 Retry、Replay、Resend、Prune 或任何 Domain mutation；
- Dashboard 的 Observations 页面只显示 public snapshot、路由结果和真实投递状态。`unknown` 明确表示最终结果不确定，不会触发自动重试。

## Durable schema

`internal/platformdb/migrations/006_observation.sql` 是 v6 的 authoritative migration。001–005 保持 immutable。`observed_events`、`route_observations`、`delivery_observations` 只保存 allowlisted public snapshot 和路由/投递结果；不保存 raw response、headers、cookie、token、credential 或 renderer/template 对象。

v5 → v6 仍遵守既有 pre-migration backup gate：先完成 inspection 和 backup，再执行 006；backup 失败时 live schema 保持 v5。Retention 默认 90 天，由 Recorder 启动的延迟、每 24 小时一次的 bounded janitor 逐批删除 root event；prune 失败只增加脱敏计数，不影响 Legacy 或 `/readyz`，外键级联删除 route/delivery facts。

## Hooks

- Source hook 位于稳定 Event 进入 per-target routing/filter 之前；
- Route hook 位于 deterministic routing/filter 结果之后；
- Delivery hook 位于真实 Messenger 结果之后；Telegram 等没有明确结果的路径记录 `unknown`；
- 全局桥默认 Noop，只有 platform owner 显式安装 Store-backed Recorder 时才写 SQLite。

详见 [P2A Passive Observation](./P2A_PASSIVE_OBSERVATION.md) 与 [Phase 2 Final Acceptance](./PHASE2_FINAL_ACCEPTANCE.md)。
