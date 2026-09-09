# P1B — Admin Bootstrap & Auth

P1B 在 P1A 的 SQLite owner 之上提供最小的管理员初始化、登录、服务端
Session、CSRF 和健康检查边界。它不接入订阅、Connector、AI、Secret Store 或
Dashboard 业务 API，也不改变 Legacy WSa 的采集、BuntDB、模板和 OneBot 投递路径。

## Migration 004

`internal/platformdb/migrations/001_core.sql`、`002_contracts.sql` 和
`003_contract_guards.sql` 都是 immutable。P1B 只新增有序的
`004_admin_auth.sql`，fresh database 按 `1 → 2 → 3 → 4` 创建 latest schema，v3
database 按 P1A 的 pre-migration backup gate 先生成仍为 v3 的快照，再在一个事务中
应用 v4。

v4 新增：

- `administrators`：唯一管理员，`singleton = 1` 和唯一约束共同保护单管理员不变量；
- `setup_state`：单例初始化状态。成功创建管理员后永久标记 `completed = 1`；
- `setup_tokens`：单例 Setup Token，只保存 SHA-256 hash、创建/过期/消费时间；
- `sessions`：服务端 Session，只保存 Session token hash、管理员关系、过期/撤销时间、
  CSRF server-side material 和 User-Agent metadata hash。

每个 migration 独立保存 `sha256:<embedded SQL>` checksum；checksum、name、future
schema、unknown/missing history 都在任何 schema mutation 前校验。

## Bootstrap 与 Setup

启动时 `auth.Service.Bootstrap` 只在 `setup_state` 尚未完成且不存在有效 token 时生成
一个 256-bit 随机 token。数据库只落 SHA-256 hash，token 有效期固定 30 分钟；重启不会
轮换仍有效的 token，也不会把已消费 token 重新打开。原始 token 只由启动路径通过专用
bootstrap stdout/container-console 边界输出，绝不会进入 API、health/readiness、普通日志、
错误 envelope 或数据库。

`POST /api/v2/setup` 需要：

- `Origin` 校验；
- Setup Token；
- 用户名规范化和唯一管理员约束；
- 最少 12 个 Unicode rune、最多 1024 bytes 的密码策略（密码值不 trim）；
- 在一个 SQLite transaction 内验证 token、插入管理员、消费 token、永久完成 setup。

重复消费、过期 token、并发 setup、第二个管理员和伪造 Origin 都不会创建第二个管理员。

## Password 与 Login

密码使用标准 Argon2id encoded hash：

```text
$argon2id$v=19$m=65536,t=3,p=2$<random-salt>$<derived-key>
```

参数集中定义在 `internal/auth/password.go`，每次 hash 使用新的随机 salt，验证使用
constant-time compare。`POST /api/v2/auth/login` 不要求预先存在 CSRF Session；它只
校验 Origin（Origin 存在时必须匹配配置）、使用 bounded failure rate limiter（默认
每个 remote/username key 5 次 / 5 分钟，最多 4096 个 key），并对未知用户名、错误密码、
禁用管理员返回相同的外部错误。

认证成功后只创建服务端 Session。原始 32-byte Session token 只进入 HttpOnly Cookie，
HTTP JSON 不回显 token；SQLite 只保存 SHA-256 hash。默认 Cookie 名为
`ddbot_ai_session`，`Path=/`、`HttpOnly`、`SameSite=Lax`，`Secure` 由部署配置显式决定。

## CSRF、Origin 与 Logout

- Setup 是 Setup Token + 必需 Origin 的独立契约；
- Login 不需要预先 CSRF；
- Logout 是已认证 mutation，必须同时通过 Origin、有效 Session 和
  `X-CSRF-Token` constant-time 校验；
- `/api/v2/auth/session` 返回当前用户名、过期时间和 CSRF token，永不返回 Session
  原文；
- Logout 先持久化 revoke，再发送清除 Cookie；无效/已撤销 Session 不会重新激活状态；
- `null`、跨站 Origin 和未明确受信的 forwarded headers 均被拒绝/忽略。

当前 P1B 只提供：

```text
POST /api/v2/setup
GET  /api/v2/setup/status
POST /api/v2/auth/login
GET  /api/v2/auth/session
POST /api/v2/auth/logout
GET  /healthz
GET  /readyz
```

响应使用稳定的 `data`/`error` envelope；错误响应只给公开错误 code/message 和 request
ID，不携带 SQLite 路径、argon hash、Session、Setup Token 或其他 Secret。

## Health / Readiness 与 Legacy fail-open

`/healthz` 在 SQLite/Auth 不可用时仍返回 HTTP 200，并将检查标记为 degraded；
`/readyz` 在 SQLite/Auth 不可用或 schema 不完整时返回 HTTP 503。`setup_required` 是
全新实例的合法产品状态，readiness 仍可为 HTTP 200。平台初始化失败只让 P1B endpoint
返回 `auth_unavailable`，Legacy Core 继续独立运行。

原生运行默认仍为 `127.0.0.1:15631`；容器由 `DDBOT_AI_HTTP_LISTEN=0.0.0.0:15631`
显式选择容器监听，Compose 是否向宿主机发布端口由部署层决定。平台数据库可以用
`DDBOT_AI_PLATFORM_DB` 指定；未指定时，启用 Admin server 的进程使用当前目录下的
`ddbot-ai.sqlite`，仍由唯一 `platformdb.Store` 持有。

## P1B 边界

本切片明确不包含：Argon2 之外的 Secret Store、AES-256-GCM、Setup/Auth 的 Vue 页面、
完整 `/api/v2` Domain API、Idempotency middleware、AI Provider/Worker/Shadow/ENFORCE、
Normalizer Runtime、Connector/Target/Subscription runtime、JWT/refresh/OAuth/RBAC 和
任何通用 Delivery retry worker。

自动化门禁覆盖 migration 004 checksum、v3 → v4 真实 backup-before-migrate、Setup Token
hash/过期/消费/并发、Argon2id、Session/Cookie/CSRF/Origin、登录限流、health/readiness、
全仓 `go test`、`go vet`、Compatibility 21/21 semantic diff，以及三目标
`CGO_ENABLED=0` 构建。
