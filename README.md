# DDBOT-AI

DDBOT-AI 是一个面向个人、社群和机器人运营场景的多平台信息监控、语义筛选与推送管理中心。

它以锁定的 DDBOT-WSa `next-dev` 提交为采集和传统推送基线，逐步增加统一 Event、程序规则、可插拔语义分类、订阅策略、Connector 和 Dashboard。传统程序负责可靠采集、去重、模板、媒体和投递；AI 只负责理解内容并减少噪声，任何 AI 或新模块故障都必须 Fail-open，不能让原有机器人失声。

## 当前状态

仓库已完成并冻结 Phase 0 / Phase 1 基础契约阶段，冻结点分别为 `phase0-baseline`（`2030834d423e8313df4ae7a937aec0f0badbd443`）和 `phase1-baseline`（`6b1d591f388f50a31855a8f93a50c4a344e2b01e`）。当前工作在 `codex/phase2-observation`，正在实现 P2A Passive Observation Runtime；该旁路运行时只记录 Event、Route 和 Delivery 事实，不改变 Legacy WSa 行为：

- 已锁定上游基线提交 `a6364e7182ec4eee93dd78e09fe7a7efd92bffab`。
- 已建立兼容性特征清单，覆盖命令、过滤、模板、OneBot 离线队列、多分片和 Bilibili/Twitter 典型事件。
- 已建立三目标 `CGO_ENABLED=0` CI 门禁：Linux amd64、Linux arm64、Windows amd64。
- 已建立事件/分类词表、字段级策略继承、Fail-open 决策和 ClassifierRelease 指纹契约。
- FFmpeg 统一通过独立可执行文件调用，主程序不链接 FFmpeg 库。
- Phase 0 和 Phase 1 已标记为 `DONE / FROZEN`；P2A 的范围与旁路不变量见 [Phase 2 — Observation](./docs/phase2/README.md) 和 [P2A Passive Observation](./docs/phase2/P2A_PASSIVE_OBSERVATION.md)。

完整的 Phase 0 实现顺序和验收条件见 [Phase 0 兼容性基线](./compat/README.md)、[V1 不变量](./docs/architecture/V1_INVARIANTS.md) 和 [发行门禁](./docs/architecture/PHASE0_RELEASE_GATES.md)。

## 产品和制品命名

| 项目 | 名称 |
| --- | --- |
| 产品 | DDBOT-AI |
| 仓库 | DDBOT-AI |
| 二进制 | `ddbot-ai` |
| 服务 | `ddbot-ai` |
| Docker 服务/镜像 | `ddbot-ai` |
| Dashboard | DDBOT-AI |

开发期为了最小化上游改动，Go module 仍暂时使用
`github.com/cnxysoft/DDBOT-WSa`；正式公开发行前会在最终仓库地址确定后统一迁移 Module、import 和构建元数据。

## 本地构建

主程序使用纯 Go 构建，媒体能力通过 PATH 或显式配置路径调用独立 FFmpeg：

```sh
CGO_ENABLED=0 go build -trimpath -o ddbot-ai ./cmd
```

也可以使用 Make：

```sh
make build
```

Dashboard 产物在构建期由 Node/Vite 生成并嵌入 Go 二进制；运行时不需要 Node：

```sh
cd web
npm ci
npm run typecheck
npm run test
npm run build
```

原生运行时 HTTP 服务默认监听 `127.0.0.1:15631`。Docker 运行时由镜像显式设置为 `0.0.0.0:15631`，供同一容器网络中的反向代理访问；宿主机端口必须使用 loopback 映射或完全不发布。详见 [Docker 监听契约](./docs/deployment/DOCKER_LISTENING.md)。

## 兼容性门禁

新增 Observation、AI、Dashboard 或 Connector Hook 时，AI Mode 为 OFF 且系统不处于明确维护/迁移状态的行为必须与锁定基线一致。门禁比较命令回复、BuntDB 状态、过滤结果、消息段内容和顺序、队列状态、分片行为及典型平台事件输出。

任何有意改变 Legacy 行为的提交都必须同时更新规格、验收条件和经过审核的 Fixture；不能因为接入新模块而静默改变传统推送。

## 许可证和来源

DDBOT-AI 使用 AGPL-3.0。上游 DDBOT-WSa、DDBOT-ws、DDBOT 及第三方依赖的版权和许可证信息继续保留在源码及发行 Notices 中。
