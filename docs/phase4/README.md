# DDBOT-AI Phase 4 — AI Shadow

状态：**PHASE 4 DONE / FROZEN**（AI Shadow 已冻结；ENFORCE 未启用）。

Phase 4 在 Phase 3 的观察事实旁路上增加可选的语义分类层。Legacy WSa 仍负责采集、去重、过滤、模板、媒体和 OneBot 投递；AI 只读取 allowlisted `NormalizedEvent`，记录分类和策略建议。无论模型结果如何，真实 Legacy Delivery 的 `effective_action` 都是 `pass`。

## 范围

- Bilibili/Twitter deterministic Normalizer，输入只来自 Phase 2 public snapshot。
- OpenAI-compatible Chat Completions provider；非流式、单次请求、超时和响应大小上限，无 SDK 自动重试。
- Secret Store credential reference；API key 不进入 provider config、prompt、日志或 API 响应。
- `ClassifierRelease` 以语义配置指纹标识；价格、超时、并发和队列容量不进入指纹。
- 有界 Shadow queue/concurrency；同一 NormalizedEvent + Release 只有一个 durable claim 和最多一次真实 provider call。
- AI mode overlay：System → Global → Source → Target → Subscription，V1 只允许 `off`/`shadow`；`enforce` 返回 `enforce_not_available`。
- 结构化 Profile/Policy、hard-safety PASS、route evaluation provenance；Profile 不进入模型 prompt。
- token/latency/cost accounting（cost 使用 integer micros）；Evaluation dataset 区分 `real_reviewed` 与 `synthetic`。

## 安全和故障边界

输入截断、未知 taxonomy、低置信度（< 0.90）、高/关键重要度、不确定、上下文不足、prompt injection、解析/Provider/存储/队列错误全部安全 PASS。Provider 未配置不会让 `/readyz` 失败，也不会阻止 Legacy Core。Shadow 失败不等待、不重写正文、不建立通用 Delivery retry queue。

## 权威制品

- Schema：`internal/platformdb/migrations/010_ai_shadow.sql`（001–009 immutable）。
- NormalizedEvent：[`NORMALIZED_EVENT_V1.md`](./NORMALIZED_EVENT_V1.md)。
- Release/fingerprint：[`CLASSIFIER_RELEASE.md`](./CLASSIFIER_RELEASE.md)。
- Policy/mode：[`POLICY_AND_PROFILE.md`](./POLICY_AND_PROFILE.md)。
- Runtime/crash semantics：[`SHADOW_RUNTIME.md`](./SHADOW_RUNTIME.md)。
- Evaluation：[`EVALUATION.md`](./EVALUATION.md)。
- 验收：[`PHASE4_FINAL_ACCEPTANCE.md`](./PHASE4_FINAL_ACCEPTANCE.md)。

Phase 5（ENFORCE、Replay、Feedback、Release packaging）在本阶段不实现。
