# Domain Command 防护契约

DDBOT-AI 不把所有 `POST` 混成一种安全要求。认证和初始化有自己的契约；已认证、会改变业务状态的 Domain Command 才进入 CSRF/幂等规则。

| 请求类型 | CSRF | `Idempotency-Key` | 额外要求 |
| --- | ---: | ---: | --- |
| `POST /auth/login` | 否 | 否 | 认证层自己的登录防护 |
| `POST /setup` | 否 | 否 | Setup Token、Origin 校验、Token 一次性消费 |
| `test connection` | 否 | 否 | 不产生持久业务副作用 |
| 已认证只读请求 | 否 | 否 | 认证/授权 |
| 已认证 Domain Command | 是 | 视是否可重放副作用 | 认证、授权 |
| 创建/删除 Subscription、创建 Target | 是 | 是 | 7 天幂等记录 |
| 人工补发、人工重试 | 是 | 是 | 7 天幂等记录 |
| Connector Migration commit | 是 | 是 | Migration Coordinator 事务 |
| 切换 Enforce、修改 Profile、修改凭据并提交 | 是 | 是 | 7 天幂等记录 |

实现层对应 `internal/security`。未知命令必须拒绝，而不能根据 HTTP 方法猜测防护等级。

## 幂等冲突

幂等记录按认证主体和 Key 隔离，并持久化：

- 请求方法；
- 规范化的具体路径（包括资源 ID 和排序后的 query）；
- 规范化 request body 的 SHA-256；
- 原始响应状态、响应头和响应体；
- 创建时间和 7 天过期时间。

相同主体 + 相同 Key + 相同指纹返回原结果。相同主体 + 相同 Key 但任一指纹字段不同，必须返回 `409 idempotency_conflict`，绝不能错误复用第一次结果。过期后才允许再次使用 Key。
