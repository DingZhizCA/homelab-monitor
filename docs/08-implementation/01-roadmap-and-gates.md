# 分阶段实施路线与验收关卡

> **文档状态**：架构规划与实施跟踪
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 推进策略
分阶段交付，每个阶段具有可运行测试、资源约束验收和回滚方案。不得直接将全部设计一次性改造到现有运行服务。阶段仅确定先后依赖，不强制排期。

## 阶段与架构关卡
| 阶段 | 主要任务 | 退出条件 |
|---|---|---|
| P0 基线 | 仓库/文档/现网只读核查，冻结协议和既有测试 | 现有功能及运行风险可追踪 |
| P1 模块骨架 | 模块化单体边界、API Schema、认证/身份 | 基础测试、权限隔离通过 |
| P2 插件平台 | Plugin Host、SDK、签名、安装/升级/回滚、OS 隔离 | Mock Plugin 故障与资源测试通过 |
| P3 采集通道 | Agent HTTPS 对账、批量指标、Newifi Remote Host | 网络业务基线无可测退化 |
| P4 数据平台 | Metric Envelope、SQLite 候选验证、身份注册、存储配额 | 增量、缺失、重置及降级正确 |
| P5 流量归属 | IPv4/IPv6/Exit Node 各计数域和质量 | 不重复计数，不伪称完整覆盖 |
| P6 Web | Dashboard、Widget、Device、Network、权限、发布 | 多端与多角色、查询限额测试 |
| P7 告警通知 | Rule/Incident、Web Push、静默、升级兼容 | 误报/去重/失败隔离测试通过 |
| P8 发布验收 | N1/Alienware/OpenWrt 业务基线 A/B 验证 | 生产安全红线全部达标 |

## 各阶段通用交付物
架构 ADR/变更说明、接口定义、单测/集成测试、性能测量、错误与故障注入记录、回滚命令、用户权限审查及数据质量结果。

## 当前仓库进度快照（2026-10-23）

以下状态只表示当前代码和本机验证情况，不代表生产验收或设备部署。

| 阶段 | 当前状态 | 尚缺的主要关卡/依赖 |
|---|---|---|
| P0 基线 | 已完成仓库、架构文档和现有 Go 路径审查；既有工作区改动保留 | 生产设备只读核查未在本轮执行，也不是本地代码验收的替代品 |
| P1 模块骨架 | 部分完成：Agent/Server 插件控制边界、设备凭据验证、本地用户/Owner/服务端 Session、HTTPS 认证 Handler、Argon2id、Web 登录与账户管理、SQLite 用户/Session/账户、Dashboard、告警规则审计及插件 Desired/Reported 仓储均已有代码与测试；`cmd/server` 可在完整显式配置下装配这些持久化路由，默认保持关闭 | Incident 等其他业务审计仍未持久化；敏感操作再认证、更细粒度 ACL 和完整服务模块拆分未实现 |
| P2 插件平台 | 部分完成：版本化 gRPC 契约、Unix Socket Host、SDK、签名/校验、权限复验暂存、本机 grant 文件加载、强制包身份/声明权限授权门、可选指标 Schema 版本声明、基于 Host feature 的标签能力协商及 Descriptor 标签值校验、Linux 同 UID 子进程 Supervisor、专用 launcher 的硬 rlimit 与 `no_new_privs`、健康检查恢复及单前序版本回滚、必填策略驱动的连续失败指数退避与 Desired Revision 隔离、回滚/隔离有界私有状态持久化及 Desired 确认后复验恢复、编排、显式批调度及跨并发批次的 Scheduler 级总并发上限、Scheduler outcome 捕获包版本与 Collector Descriptor、Agent 规范化插件观测适配、可取消的有界关闭、可选 Agent 插件配置及控制轮询装配均有测试 | 未提供不同 UID、cgroup 进程树总量、实际文件/网络/系统调用隔离；Schema 转换和完整安全故障注入未完成；不得作为生产安全沙箱使用 |
| P3 采集通道 | 进行中：插件 Desired/Reported 控制 API、SQLite 原子仓储/审计、Owner 写入、`plugin-pack` 有容量边界的可选原子制品导入、Server 制品总字节/包数/扫描硬边界、Desired 引用保护及有界周期清理、签名制品下载和 Agent 对账，以及持久化指标批量链均已有测试；Agent staging 具备单写者锁、写前磁盘核算、总字节/包数/扫描硬边界，并沿控制轮询按 current/previous 保护集、宽限期、超时和删除量清理；Agent 批队列通过私有、有界、带校验和的原子状态文件跨重启保留快照和 pending ID；`cmd/agent` 与 `cmd/server` 两端相关能力都默认关闭并要求完整显式安全与容量参数 | Server/Agent 目标环境运行链、容量参数、清理锁等待及 Agent 状态文件写放大尚未验收；Newifi Remote Host 未完成；网络基线测试未执行 |
| P4 数据平台 | 进行中：ADR-019 已实现 CGO SQLite 双库、迁移、原子指标批次和原始 Descriptor 历史源；`core.db` 已有用户、Session、账户审计、Dashboard 与告警规则不可变修订历史、Incident 当前状态及插件 Desired/Reported；ADR-020 查询已可显式装配；Server 启动可恢复最新持久化快照；本机子进程强杀测试覆盖非空 WAL 重开、`quick_check`、控制状态和批次幂等恢复；ADR-021/022 的 PASSIVE checkpoint、保留、页上限、软硬容量和 507 背压已接入；ADR-024 的 incremental auto-vacuum、显式旧库迁移、TRUNCATE 与 incremental vacuum 已接入；ADR-025 的持久化待聚合队列、UTC 分钟/小时/日桶、四层保留和分层历史路由已有事务与边界测试 | 目标设备运行、断电与存储介质语义、聚合写放大、积压恢复、维护及容量参数调优和容量验收未完成；插件规范化指标尚未进入聚合链 |
| P5 流量归属 | 未开始 | 需要确认数据源覆盖、计数域和去重证据后再实现 |
| P6 Web | 部分完成：已有 React Dashboard、登录态前置门、设备列表/指标卡、SSE、临时跨设备对比、明确授权的磁盘刷新、网络快照、Owner 账户管理，以及 Dashboard 草稿/发布/回滚 UI；持久化认证、当前指标、分层历史查询和 SQLite Dashboard 仓储可由 `cmd/server` 显式装配，Explore 可读取有界历史且不触发采集；共享发布策略拒绝当前 Registry 之外的 Descriptor | History API 尚未返回聚合粒度元数据；系统模板持久化、敏感发布再认证、更细粒度 Viewer ACL、Network 流量归属/拓扑和 Alerts 页面仍未实现 |
| P7 告警通知 | 进行中：版本化规则结构、Descriptor/Unit/Label Selector 校验、显式表达式复杂度上限及三值 AND/OR 评估已有测试；SQLite 规则仓储持久化不可变历史、当前 Revision 和原子审计；`cmd/server` 可显式装配 Owner 管理路由及有周期、超时、规则数和状态容量边界的评估运行链；调度以稳定游标有界分页，只读取 Current State，按规则隔离失败，并以 CAS 推进可重开恢复的 Incident | 没有敏感操作再认证；阈值、Unknown/Disabled 策略和时间没有产品默认值；规则删除、旧 Incident 显式迁移、通知去重、静默、站内/Web Push 和投递失败隔离仍未实现 |
| P8 发布验收 | 未开始，且不属于本地代码任务 | 需要用户另行授权后制定计划；必须通过生产设备基线与安全红线验收，不能由本地测试替代 |

### 开发约束与停止条件

- 当前可复验检查为根模块 `go test -race ./...`、`go build ./cmd/server ./cmd/agent ./cmd/plugin-pack ./cmd/plugin-launcher`、`go vet ./...`、`git diff --check`，独立存储评测模块的单测、race、vet 和构建，以及 `web` 目录下的 `npm test`、`npm run build` 和 `npm run lint`。当前本地 Go 与 Web 检查均通过；这些结果不替代目标设备上的验证。
- Web lint 已无警告；Vite build 仍提示懒加载的 ECharts chunk 为 511 KB（gzip 173 KB），略高于 500 KB 提示线。构建通过，后续可结合实际加载体验评估图表模块拆分。仪表盘 Widget 已增加手动最新值预览，不触发采集；完整历史图表仍未接入。
- 当前 launcher 仅提供无需特权的基础硬 rlimit 与 `no_new_privs`。不具备目标主机身份、cgroup 委派和 OS 版本的本地证据时，不实现或宣称具备完整系统级插件沙箱。
- 后台维护与容量保护必须完整显式配置；当前具备事务型分钟/小时/日聚合、四层保留与查询、有界 PASSIVE/TRUNCATE checkpoint、incremental vacuum、SQLite 页上限及指标写入背压。目标环境故障恢复、聚合写放大与积压、维护与容量参数、锁等待和容量验收完成前，不将当前本地测试描述为生产验收。
- P5 的流量归属边界、P7 的告警阈值等仍标为待细化的规则，需按决策台账确认；不得为完成路线图自行虚构业务策略。
- P8 设备测试及生产部署不在当前本地工作授权范围内。

## 对 Codex/开发者的约束
优先阅读 docs/README.md 和 00-governance 的决策记录，明确当前代码与规划差距；一次只做一个变更主题；在生产设备修改之前提出待审批计划、验证点与回退方式。禁止以文档设计为由覆盖 AGENTS.md、现有用户改动或未审查配置。

## 已知现状 vs 未来目标
仓库已有 Go Agent 与 Server API，以及经本地测试的插件、持久化认证、指标分层存储、Dashboard 和告警规则/Incident 基础链。插件生命周期尚不具备系统级隔离；Network 页面只是接口快照而不是流量账本，通知投递链尚未实现。PRD 与发布说明应按各阶段状态描述，不应将规划目标或未接线能力写作已完成产品能力。
