# Connector Migration 与 `migration_held`

迁移不会冻结事件采集和 Event Persistence。迁移窗口内发现的新 Event 仍然持久化，AI 仍可完成，RouteDecision 仍然生成；受影响的 Delivery 进入持久化的 `migration_held` 状态。

## 可重启载荷

每一条 held Delivery 必须只依赖 SQLite 可恢复字段：

- `event_id`；
- `route_decision_id` 和完整 route snapshot；
- logical target（`target_id`、`target_type`、`external_id`）；
- renderer input 或已渲染的 message snapshot；
- `migration_id`；
- `delivery_id` 和 snapshot schema version。

它不得保存或引用进程内 `Notify`、`Messenger`、renderer/template 实例、channel 或 goroutine。`internal/deliverysnapshot` 定义 JSON 载荷，`internal/migration` 的测试会先序列化、模拟进程重启，再解码释放。

## 释放规则

Migration Coordinator 只释放本次 `migration_id` 持有的行：

1. 迁移成功：将当前迁移选定的新 route snapshot 写入载荷，然后释放到正常 delivery path。
2. 迁移失败或恢复旧 Connector：使用旧 route snapshot 释放到旧 delivery path。
3. 进程在迁移中崩溃：启动时先恢复迁移状态，读取 `migration_held` 行，再按成功/失败结果释放。
4. 该状态仅服务 Connector Migration，不是通用持久化重试队列。

`unknown` Delivery 永不自动重发；只有 `migration_held` 在 Coordinator 明确完成迁移后才可释放。
