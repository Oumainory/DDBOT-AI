# DDBOT-AI V1 不变量

这些不变量是每个阶段实现和评审的硬门禁。它们不是产品愿景；如果一个变更无法证明不破坏下列条件，就不能合并到 V1 主线。

1. 未启用 AI 时，原 WSa 可观察行为不得因 DDBOT-AI 新模块发生语义变化。
2. 任何 DDBOT-AI 新模块故障不得阻止 Legacy 采集与原有可靠推送，除非管理员正在执行明确的维护或迁移操作。
3. BuntDB 在 V1 始终是 Legacy Subscription 与群配置的唯一权威源；SQLite 不得反向成为第二真相源。
4. 同一 Normalized Event / Classifier Release 最多产生一次真实模型调用。
5. AI 只能把满足 Enforce Eligibility、有效存储契约和 Effective Policy 的 Route 变成 DROP；任何不确定状态均 PASS。
6. AI 不得改写正常推送正文。
7. Target 身份不明确时必须拒绝绑定或迁移，不允许猜测。
8. `unknown` Delivery 永不自动重发。
9. Secret 永不通过普通读取 API、日志、WebSocket 或 Audit 输出明文。
10. Dashboard 与聊天命令修改 Legacy 配置必须调用同一 Service。

## 变更检查

- 任何新增 Observation、AI、Delivery 或 Connector Hook 都必须运行 `compat/fixtures/` 的基线比较。
- 任何有意改变 Legacy 行为的提交必须同时更新规格、验收条件和经过审核的 Fixture。
- Fail-open 只允许将 AI 故障降级为原始 PASS；它不允许绕过确定性的安全拒绝、Target 身份校验或权限校验。
