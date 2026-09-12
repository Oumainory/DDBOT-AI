# Phase 4 Final Acceptance

状态：**PHASE 4 DONE / FROZEN**。

完成门禁：

- NormalizedEvent v1、Bilibili/Twitter deterministic normalizer 和 bounded public input；
- OpenAI-compatible non-streaming provider、structured JSON schema/object、timeout/response bound、无自动 retry；
- Secret Store credential reference、provider test connection、release fingerprint 和版本化 prompt/schema；
- durable event/release claim、call-start marker、crash-before/after-call 语义；
- bounded async Shadow、OFF 零调用、SHADOW event-level 单调用、多 route policy evaluation；
- Profile、System→Global→Source→Target→Subscription mode/policy overlay、hard-safety PASS、provenance；
- suggested action 与 effective PASS 分离，Legacy 正文/目标/顺序不变；
- token/latency/integer-micros cost accounting、Evaluation real/synthetic 区分、readiness 只报告不启用；
- Provider/Profile/Policy/Shadow/Evaluation Dashboard API 和 AI 页面；
- Phase 3 回归、Compatibility baseline/current 21/21 semantic diff 0、frontend gates、Go/vet、adapter、三目标 `CGO_ENABLED=0` build、Fake Provider CI 全部通过。

Phase 4 只允许 `OFF`/`SHADOW`。任何尝试激活 `ENFORCE` 必须得到 `enforce_not_available`；本阶段不实现 Replay、Feedback、Media Cache、通用重试队列或 Release packaging。

权威新增 migration 为 `010_ai_shadow.sql`；001–009 保持 immutable。Phase 4 冻结后创建工程 tag `phase4-baseline`，将 `main` fast-forward 到同一提交，并从该点创建空的 `codex/phase5-enforce-release`。
