# DDBOT-AI Phase 2 Final Acceptance

状态：**PHASE 2 IMPLEMENTATION DONE / READY FOR FREEZE**

Phase 2 建立在 `phase1-baseline`（`6b1d591f388f50a31855a8f93a50c4a344e2b01e`）之上。本阶段完成 P2A passive write path 的消费、诊断和验收，但不创建 `phase2-baseline`，也不修改 `main`。

## Scope

Phase 2 只记录和读取已经发生的 Event、Route、Delivery 事实。Observation 是 fail-open auxiliary subsystem，不拥有 Legacy Subscription、BuntDB、过滤、模板、媒体、Messenger 或 retry 决定。

明确 deferred：Source/Target/Subscription、Connector Management、Telegram Pairing、Connector Migration、Bilibili/Twitter Discovery、NormalizedEvent、AI Provider、Classifier、Profile/Policy、Shadow、ENFORCE、Feedback、Replay、Media Cache 和通用 Retry Queue。

## P2A write-path evidence

- migration `006_observation.sql` 保持 immutable，latest schema 为 v6；
- bounded single-worker Recorder 使用同一个 `platformdb.Store`，不打开第二 SQLite connection；
- event snapshot 是固定 allowlist，保存 SHA-256 fingerprint，不保存 raw response、credential、header、cookie、token 或 renderer object；
- queue full、SQLite failure、worker panic、prune failure 都只产生脱敏计数，Legacy 调用继续 fail-open；
- Legacy Event/Route/Delivery hook 未改变原有过滤、渲染、发送顺序或返回值。

## Read Repository and API

`internal/platformdb.ObservationRepository` 是唯一 Observation read boundary。所有 query value 使用参数绑定；没有 OFFSET 或全文搜索。

Routes：

- `GET /api/v2/observations/events`
- `GET /api/v2/observations/events/{id}`
- `GET /api/v2/observations/events/{id}/routes`
- `GET /api/v2/observations/events/{id}/deliveries`
- `GET /api/v2/observations/summary`

所有接口都需要现有 HttpOnly Session，全部是 read-only GET，不需要 CSRF。错误只返回稳定的 `invalid_argument`、`unauthorized`、`observation_not_found` 或 `observation_unavailable`，不会返回 SQL、数据库路径、snapshot 内部错误或 stack trace。

## Query and pagination

Event list 支持 `platform`、`event_type`、`source_kind`、`source_external_id`、RFC3339 `from`/`to`；`from > to` 或非法时间返回 400。默认 `limit=50`，硬上限 200，非法范围返回 400。

Cursor 是 base64url(JSON) 的 version 1 payload：`{"v":1,"t":<observed_at Unix seconds>,"id":"<event id>"}`。排序固定为 `observed_at DESC, id DESC`，下一页使用 `(observed_at < t) OR (observed_at = t AND id < id)`。Cursor 不含 secret，retention 删除 cursor 对应行不会导致 500。现有 v6 的 observed_at/platform-source/event indexes 足够支撑当前 bounded queries，因此没有新增 migration 007。

List DTO 只返回事件元数据、fingerprint、截断的 `public_summary` 和 route/delivery 计数；detail 才返回单事件的 allowlisted `public_snapshot`，并聚合 Route/Delivery timeline。未知 JSON 字段永远不会被前端 dump。

## Runtime stats and retention

Summary 明确区分：

- runtime counters：当前进程接受数、queue capacity/depth、queue dropped、persistence/worker/prune errors；
- database facts：最近 24 小时事件、路由和投递数量。

Recorder 启动一个延迟 retention janitor，默认 90 天、每批最多 256 个 root event，之后约每 24 小时执行一次。prune failure 不影响 `/readyz`、Dashboard 或 Legacy，也不创建通用重试队列。Runtime status 为 `available`、`degraded`、`disabled` 或 `unknown`。

## Dashboard

导航为 `Overview → Observations → About`。Observations 页面提供平台/事件类型/source/time filters、cursor Load More、空状态和 unavailable 状态；点击事件打开 detail drawer，展示 allowlisted snapshot、Route timeline 和 Delivery timeline。`filtered` 表示 Legacy deterministic filter，不能误写成 AI DROP；`unknown` 明确显示“结果未知（不会自动重试）”。没有 Retry、Replay 或 Resend 控件。

## Security and fail-open

Read API 和 Dashboard leak tests 覆盖测试 secret、token、cookie、ciphertext、nonce、Master Key、数据库路径和 raw error。Observation failure 不会阻止 Legacy Messenger send，正常消息内容、顺序和返回状态不由 Observation 反向改变。

## Acceptance matrix

| Gate | Result |
| --- | --- |
| Observation repository read/query | PASS |
| Cursor duplicate/loss and concurrent insert tests | PASS |
| Event → Route → Delivery integration | PASS |
| Filtered route / unknown no-retry semantics | PASS |
| Retention delayed janitor and fail-open | PASS |
| Authenticated-only API and sanitized errors | PASS |
| Dashboard list/detail/empty/unavailable flow | PASS |
| migration 006 checksum and schema | unchanged / v6 |
| AI / Normalizer / Source / Target / Connector runtime | not implemented |

Phase 2 完成后仍需单独执行机械 Git freeze；本阶段不创建 `phase2-baseline`，不启动 Phase 3。
