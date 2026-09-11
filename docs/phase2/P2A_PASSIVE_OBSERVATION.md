# P2A — Passive Observation Runtime

## 不变量

1. Observation 永远是旁路事实记录，不参与 Legacy 的过滤、模板、发送、重试或订阅决定。
2. 未安装 Recorder 时，全局观察桥是无副作用 Noop。
3. Recorder 的入口不阻塞 Legacy：bounded queue 使用 non-blocking `select`；队列满即丢弃该事实并增加计数。
4. 只有有效的 event trace 才能生成 route trace，只有有效的 route trace 才能生成 delivery observation，因此不会主动制造孤立 route/delivery 行。
5. SQLite 失败、worker panic、关闭超时和 retention prune 失败均 fail-open；不创建 durable retry queue。
6. `platformdb.Store` 是唯一 SQLite owner；Recorder 复用它，不调用第二次 `sql.Open`。

## 记录内容

### ObservedEvent

每个上游事件最多在稳定的 Legacy Event seam 生成一次记录。字段包括平台、source kind/ID、upstream event ID、event type、观察时间、source event time、SHA-256 content fingerprint 和固定 allowlist 的 public snapshot。snapshot 的字段集合由 `internal/observation/snapshot.go` 中的结构体显式定义；不会通过 `json.Marshal` 直接保存上游响应。

Fingerprint 只用于观察和后续诊断，不是去重、过滤或投递授权依据。

### RouteObservation

每个有明确订阅路由的 Notify 生成一个 route ordinal。结果限定为 `pass`、`filtered`、`skipped` 或 `unknown`，reason/result 只接受稳定的小写标签。过滤失败、缺少 Concern 或队列已满时，invalid trace 会令后续 hook 变成 no-op。

### DeliveryObservation

Delivery 只在 route trace 有效且实际进入 Messenger/connector 结果边界后生成。状态限定为 `sent`、`queued`、`not_sent`、`unknown`、`rejected`。OneBot 的 `SendResp.Status()` 直接映射；没有明确结果的 connector 使用 `unknown`，永不猜测为 sent。

## 队列、worker 与关闭

Recorder 启动一个 worker，按 FIFO 顺序写入 event → route → delivery。`Close(ctx)` 先停止接收，再 drain 当前 bounded queue；ctx 超时会取消 worker context 并丢弃剩余事实，不延迟 Legacy shutdown。worker 有 recover boundary，错误只通过稳定的 component/class/counter 诊断（默认日志也只输出这三个值），不输出 payload、SQL、token 或 secret。

## Retention

默认 retention 是 90 天。`PruneOnce` 以 `observed_at` 和 ID 排序、每批最多 256 个 root event；SQLite 外键级联删除相关 route/delivery。它是显式 best-effort maintenance，不是 readiness 条件，也不重试失败操作。

## SQLite migration

当前 platform schema 为 v6。006 是独立 checksum 的 immutable migration；fresh database 按 001 → 006 顺序创建，v5 数据库启动时先执行 pre-migration backup，再应用 006。latest 数据库不会产生无意义 backup，backup failure 不会执行任何 v6 schema mutation。

## Legacy seams

当前接线保持最窄范围：

- `lsp/concern.StateManager.DefaultDispatch` 在事件进入 per-target routing 前发出 event observation，并在现有 `filterNotify` 中记录 deterministic route outcome；
- `lsp.ConcernNotify` 只负责把有效 route trace 带到真实通知发送边界；
- `lsp.sendGroupMessage`/forward path 在 `adapter.SendResp.Status()` 或明确 forward error 返回后记录 delivery；Telegram 无明确结果时记录 unknown。

这些 hook 不改返回值、过滤顺序、模板内容、消息分片、离线队列或 Messenger 调用参数。P2A 不提供 Observation API/页面；后续阶段才能消费这些事实。
