# Phase 1 验收契约

本文件定义 **DDBOT-AI Phase 1 — Platform Foundation** 的完成门槛。文档描述的是必须达到的状态，不代表当前分支已经完成实现。除本文件外，Phase 0 的兼容性 Fixture、语义 diff 和三目标纯 Go 构建门禁仍然有效。

## 1. AI OFF 兼容性保持 21/21

在 AI OFF、没有明确维护/迁移操作的条件下：

- 锁定的 Legacy baseline 测试继续通过；
- compatibility command 真正执行 Fixture，并比较命令回复、BuntDB 状态、过滤结果、消息段及顺序、OneBot 队列状态、多分片行为和典型 Bilibili/Twitter 事件；
- 当前实现与 baseline 的 semantic diff 为零。

验收证据：CI 中的 baseline tests、current tests、compatibility diff 和 manifest gate 全部成功。

## 2. 新 SQLite 故障不影响 Legacy

删除、不可访问或暂时无法打开新的 SQLite 数据库后：

- Legacy WSa 仍可按既有方式启动和工作；
- Legacy 采集、BuntDB 订阅和原有可靠推送不因 Dashboard 存储失败而停止；
- SQLite 相关能力报告明确的 health/readiness 状态，并按既定 fail-open 边界降级。

验收证据：独立的 SQLite 缺失/故障测试，以及 Legacy 启动和推送 Fixture 的回归结果。

## 3. 一次性 Setup Token 与唯一管理员

全新实例必须满足：

- `/setup` 只能凭有效的一次性 Setup Token 完成初始化；
- 初始化创建唯一管理员；
- Token 消费是原子的，成功消费后永久失效；
- 初始化完成后 `/setup` 不再可用；
- 重放旧 Token、并发消费或伪造 Origin 均被拒绝，且不会创建第二个管理员。

验收证据：Setup Token 单次消费、重放、并发、Origin 校验和唯一管理员测试。

## 4. Session、Cookie 与 CSRF

- 登录成功后使用服务端 Session 和 HttpOnly Cookie；
- 登录请求本身不要求调用方预先拥有 CSRF Session；
- 已认证且会改变业务状态的 Domain Command 必须通过 CSRF 校验；
- 认证/初始化接口使用各自独立的防护契约；
- Session 失效、退出登录、过期和 CSRF 失败都返回稳定、可区分且不泄露敏感信息的错误。

验收证据：登录、Cookie 属性、logout、过期 Session、CSRF 缺失/错误和合法写操作测试。

## 5. Secret Store 与 Recovery

- Secret 使用 AES-256-GCM 加密后落盘；
- 普通读取 API、日志、WebSocket、Audit 和错误响应永远不返回明文 Secret；
- 密文包含可验证的版本/nonce/认证标签信息，篡改会被拒绝；
- Master Key 丢失但数据库已有密文时，实例进入 Recovery；不得静默生成新 Key 覆盖或使旧密文看似有效；
- Recovery 状态可被 health/readiness 和 Dashboard 正确观察。

验收证据：加解密、篡改、明文泄露扫描、Master Key 丢失和 Recovery 状态测试。

## 6. `/healthz` 与 `/readyz`

- `/healthz` 表示进程仍能响应基本健康检查；
- `/readyz` 表示提供 Dashboard/API 所需的基础依赖已准备就绪，并能区分 SQLite、Session/Auth、Secret Store 等依赖故障；
- Dashboard/API 不可用时，Legacy 采集和推送链仍可运行；
- 检查接口不返回 Secret、Session 或其他敏感配置。

验收证据：进程存活、依赖未就绪、Recovery、数据库不可用和 Legacy 并行运行测试。

## 7. Vue Dashboard 最小流程

Vue 3 + Element Plus + Router + Pinia 的 Dashboard shell 必须能够在浏览器中完成：

```text
Setup → Login → Overview Shell → Logout
```

要求：

- 页面能显示 Legacy Core、Admin API、SQLite、Secret Store 的基础状态；
- 能显示版本、commit 和 AGPL 信息；
- 刷新页面后的认证状态遵守服务端 Session 结果；
- Node 只参与构建，不作为生产运行时依赖。

验收证据：构建产物检查和浏览器端 Setup/Login/Overview/Logout smoke test。

## 8. 三目标纯 Go 构建

以下发行门禁必须继续通过：

```text
CGO_ENABLED=0  linux/amd64
CGO_ENABLED=0  linux/arm64
CGO_ENABLED=0  windows/amd64
```

FFmpeg 始终通过独立可执行文件和 `os/exec` 边界调用；不得为了媒体能力把 FFmpeg 库重新链接进主程序。

验收证据：CI 三目标构建 job 的成功结果和可复现的构建命令。

## Phase 1 完成定义

只有当以上八项均有自动化或可审计的验收证据，且 Phase 0 compatibility gate 未回退时，才能把 Phase 1 标记为完成。任何 AI Provider、Normalizer Runtime、Shadow、ENFORCE、Connector Migration Runtime 或 SQLite Subscription Primary 的接入都属于后续 Phase，不得以“顺手实现”为由纳入本阶段。
