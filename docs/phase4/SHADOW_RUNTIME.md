# Shadow Runtime

Runtime 使用有界 channel queue（默认容量 64）和有界 worker concurrency（默认 2）。`Schedule` 只持久化 allowlisted NormalizedEvent 并尝试非阻塞入队；队列满、SQLite 错误或 Normalizer 错误都不会阻塞 Legacy send，必要时记录安全的 `queue_dropped`/error 计数。

只有 Legacy deterministic route outcome 为 pass 且至少一个 route 的 effective mode 为 `shadow` 时才分类。全部 route 为 `off`/filtered/skipped 时不调用模型，可保存 `ai_mode_off` route evaluation。多个 Target/Profile 对同一 Event 共享一个 event-level Decision，之后每个 route 运行本地 Policy。

## Durable at-most-once

`ai_decisions(normalized_event_id, classifier_release_id)` 唯一键首先建立 queued row，再原子 claim 为 running；发送 HTTP 前必须持久化 `call_started_at`。重启时：

- claim 存在但 marker 尚未写入：恢复为 queued，可再次调用一次；
- marker 已写入但完成结果不存在：恢复为 `uncertain_call`，effective PASS，绝不重试；
- Provider timeout、429、5xx、断连、无效 JSON/schema 都只结束本次 Decision，不自动重试。

成功、错误和不确定状态都写 token（若 provider 提供）、latency、provider/model、stable error code 和当时的 cost snapshot。未知 usage 不猜 token；cost 使用 integer micros，不用浮点金额。

## Legacy boundary

AI worker 不在 Messenger 调用链上等待，永不改写消息正文、标题、图片、目标或发送顺序。Phase 4 `effective_action` 数据库约束固定为 `pass`；AI 只能提供 Dashboard 可观察的 suggested action。没有 Provider 时 runtime 仍启动并以 `provider_unavailable`/PASS 运行，`/readyz` 不因 AI 降级失败。
