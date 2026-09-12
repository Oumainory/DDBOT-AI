# AI Mode、Profile 与 Policy

## Mode overlay

Mode 只覆盖显式值，按 `System → Global → Source → Target → Subscription` 解析；`inherit` 保持上一层，全部继承时默认 `shadow`。`off` 不产生 provider call，`shadow` 异步记录建议。`enforce` 仅在类型/schema 中可表示，Phase 4 的所有写 API 均返回 HTTP 409 `enforce_not_available`，没有可用开关。

## 结构化 Profile

Profile 不是 prompt，保存 `id/name/description/default_action/category_actions/tag_actions/safety`。内置 `builtin.official_game` 对 `promotion`、`giveaway`、`community`、`personal_update`、`repost` 和普通 `event` 提供 DROP 建议；Profile 只在本地评估，不会导致针对不同 Target 的额外模型调用。被引用的 Profile 不能删除。

## Policy priority

`ResolvePolicy` 是无副作用纯函数，优先级固定为：

1. hard-safety PASS；
2. matching tag PASS；
3. matching tag DROP；
4. category action；
5. profile default（否则 PASS）。

未知 category/tag 不得触发 DROP；tag 冲突时 PASS 胜出。`high`/`critical`、`unknown`、confidence < 0.90、`uncertain`、`insufficient_context`、`truncated`、prompt injection、无效分类、缺少 policy context 和存储/Provider错误全部 hard PASS。

每一条 `ai_route_evaluations` 保存 effective mode/profile、classification/policy suggested action、始终为 `pass` 的 effective action、hard-pass reason 和 provenance JSON。Shadow 的 DROP 只是可解释的建议；真实 Legacy Messenger 始终按原模板、正文、目标和顺序发送。
