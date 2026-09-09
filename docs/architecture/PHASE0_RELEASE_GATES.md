# Phase 0 发行门禁

Phase 0 的目标是固定 Legacy 行为和可交付的构建边界，而不是提前实现 Dashboard、SQLite Primary 或模型供应商。

## 纯 Go 门禁

CI 和发行工作流必须在 `CGO_ENABLED=0` 下构建：

- Linux amd64；
- Linux arm64；
- Windows amd64。

任何依赖偷偷引入 CGO 都必须让发行失败。FFmpeg Full 能力只能作为独立 runtime 可执行文件调用；主程序不链接 FFmpeg C 库，也不把静态 FFmpeg 放入 Go binary。

## 兼容性门禁

`compat/manifest.json`、`compat/fixtures/*.json` 和现有 upstream 测试共同构成基线。每次插入 Observation、AI、Delivery 或 Connector Hook 时，比较：

- `/watch`、`/unwatch` 和 FilterHook；
- 模板渲染、媒体顺序和消息正文；
- OneBot 离线队列、过期和 `unknown` 语义；
- 多分片顺序以及部分失败行为；
- Bilibili/Twitter 典型事件映射。

时间戳、随机请求 ID、临时路径等只按 fixture 声明的规则规范化。不能为了让 diff 通过而忽略正文、Target 身份、发送次数、分片顺序、过滤结果或 BuntDB 状态。
