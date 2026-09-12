# Evaluation Dataset 与 Runs

Evaluation case 保存独立的 NormalizedEvent public snapshot、expected importance/action、critical 标记、notes/source 和 `label_kind`。`label_kind` 只能是 `real_reviewed` 或 `synthetic`；没有真实人工标注时不伪造 reviewed 数量。Observation 清理不会删除 case，dataset 只支持显式删除。

创建 Evaluation Run 时选择一个 ClassifierRelease 和 case id 集合（空集合表示全部）。Runner 逐 case 调用一次 provider，使用内置 Profile 做本地 policy suggested action，写入 parse/provider error、comparison、latency、cost 和分类 JSON。Provider 无法使用时 case 安全 PASS/error；不会重试，也不会影响 Legacy 或 readiness。

Run metrics 包括 parse success、drop precision、important false-drop、总 cost/latency 和 case counts。`GET /api/v2/ai/enforce-readiness` 只展示未来 Phase 5 的门槛：真实 reviewed regression ≥100、important pass ≥40、Shadow decisions ≥200、reviewed suggested DROP ≥50、parse success ≥99%、drop precision ≥90% 等；数据不足显示 NOT READY，不得用 synthetic 填数。Phase 4 没有启用 ENFORCE 的 API。

Evaluation API 不返回 provider raw response、prompt、API key、cookie、session 或其它凭据。
