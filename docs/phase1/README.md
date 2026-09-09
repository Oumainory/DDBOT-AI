# DDBOT-AI Phase 1 — Platform Foundation

状态：**范围已冻结；P1A SQLite Foundation 正在实施，P1A.1 契约收口已完成**。

Phase 1 以 `phase0-baseline`（`2030834d423e8313df4ae7a937aec0f0badbd443`）为稳定锚点，建立 DDBOT-AI 的安全、存储、API 和 Dashboard 基础。Phase 1 不改变 Legacy WSa 的采集、过滤、模板或 OneBot 投递语义；所有新基础设施都必须遵守 [V1 不变量](../architecture/V1_INVARIANTS.md) 和 Phase 0 兼容性门禁。

## 目标

交付一个可以安全初始化、登录、查看基础状态并退出的最小平台基础层：

```text
一次性 Setup Token
        ↓
唯一管理员创建
        ↓
服务端 Session + HttpOnly Cookie
        ↓
CSRF 保护的已认证业务写操作
        ↓
SQLite / Secret Store / API 基础设施
        ↓
Vue 3 Dashboard Shell
```

Phase 1 完成时，Dashboard 至少能够完成：

```text
Setup → Login → Overview Shell → Logout
```

整个运行时不依赖 Node.js；Node 仅可作为构建工具使用。Legacy 采集和推送可以在 Dashboard/API 不可用时继续工作。

## 范围内

### 存储与安全基础

- SQLite 基础设施和版本化 migrations。
- Setup Token：一次性、受保护、消费后永久失效。
- 唯一管理员创建和 Admin Auth。
- 服务端 Session，Session Cookie 必须为 HttpOnly，并按部署要求设置 Secure/SameSite 属性。
- CSRF 防护。登录本身不要求预先存在 CSRF Session；已认证且会改变业务状态的 Domain Command 必须遵守 [命令保护契约](../architecture/COMMAND_PROTECTION.md)。
- Secret Store：AES-256-GCM 落盘、密文不可由普通 API 读回、Master Key 丢失且已有密文时进入 Recovery，而不是静默生成新 Key。

### API 与运行状态

- `/api/v2` 路由和错误响应的最小骨架。
- `/healthz` 与 `/readyz` 的清晰区分。
- About、版本、commit、许可证和 AGPL 信息。
- Dashboard/API 失败不能拖垮 Legacy 采集或可靠推送。

### Dashboard Shell

- Vue 3 SPA shell。
- Element Plus 基础组件。
- Router 和 Pinia 的最小接线。
- Setup、Login、Overview、Logout 的基础页面和状态流转。
- Dashboard 不承载平台订阅、AI 配置或投递控制逻辑；那些属于后续 Phase。

## 明确不在 Phase 1

以下能力即使已有 Phase 0 的契约或类型，也不能在 Phase 1 接入 Legacy runtime、启动路径或 API：

- Classifier Provider、AI Worker、Shadow、Profile Runtime、AI Route Hook、ENFORCE。
- Bilibili/Twitter Normalizer Runtime。
- Connector、Target、Subscription 的管理运行时。
- Connector Migration Runtime。
- SQLite 成为 Legacy Subscription 的 Primary 或第二真相源。
- 新的自动重试队列、AI 语义调用、模型成本计费或正文改写。

Phase 0 中已经存在的 `ClassifierRelease`、Policy、Migration Snapshot 等纯契约可以继续被测试或文档引用，但不得因为 import、`init()`、全局注册或启动路径接线而改变 WSa 行为。

## 不变量与实施约束

1. `phase0-baseline` 标签及其提交保持不变；Phase 1 的每次验证都必须保留 AI OFF 兼容性 21/21 门禁。
2. 新 SQLite 数据库是 Dashboard 基础设施的存储，不得反向成为 Legacy Subscription 的权威源。
3. 新模块故障必须在既定 fail-open 边界内处理；除明确维护/迁移操作外，不得阻止 Legacy 采集和推送。
4. Secret 不得通过普通读取 API、日志、WebSocket、Audit 或错误响应泄露明文。
5. 所有 Domain Command 的认证、Origin、CSRF、参数校验和幂等边界必须可测试；认证/初始化接口使用各自独立的防护契约。
6. 三目标 `CGO_ENABLED=0` 构建门禁继续有效；FFmpeg 仍作为独立 runtime 进程调用。

Phase 1 的逐条验收条件见 [ACCEPTANCE.md](./ACCEPTANCE.md)。

当前实施切片为 [P1A — SQLite Foundation](./P1A_SQLITE_FOUNDATION.md)。
