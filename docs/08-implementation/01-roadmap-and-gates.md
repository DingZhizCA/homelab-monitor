# 分阶段实施路线与验收关卡

> **文档状态**：架构规划（尚未实施）
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

## 当前仓库进度快照（2026-10-18）

以下状态只表示当前代码和本机验证情况，不代表生产验收或设备部署。

| 阶段 | 当前状态 | 尚缺的主要关卡/依赖 |
|---|---|---|
| P0 基线 | 已完成仓库、架构文档和现有 Go 路径审查；既有工作区改动保留 | 生产设备只读核查未在本轮执行，也不是本地代码验收的替代品 |
| P1 模块骨架 | 部分完成：Agent/Server 插件控制边界、设备凭据验证、`internal/auth` 用户/Owner/服务端 Session 服务、HTTPS 认证 Handler、Argon2id verifier、同源登录/注销/首次 Owner 初始化 UI、账户管理 UI 和原子最后 Owner 保护均已有代码与测试；SQLite 用户、Session 和账户审计仓储已实现同事务审计、当前 Owner 复核、重开恢复及并发首次 Owner 保护；Owner 插件 Desired State Handler 和 `internal/server.NewRouter` 已有边界 | AuthHandler 尚未接入 `cmd/server` 的显式 Origin、Session TTL 和限速配置；插件 Desired State、Dashboard 和其他业务审计仍为内存实现；敏感操作再认证、Viewer 字段级授权和完整服务模块拆分未实现 |
| P2 插件平台 | 部分完成：版本化 gRPC 契约、Unix Socket Host、SDK、签名/校验、权限复验暂存、本机 grant 文件加载、强制包身份/声明权限授权门、可选指标 Schema 版本声明、基于 Host feature 的标签能力协商及 Descriptor 标签值校验、Linux 同 UID 子进程 Supervisor、健康检查恢复及单前序版本回滚、必填策略驱动的连续失败指数退避与 Desired Revision 隔离、编排、显式批调度及跨并发批次的 Scheduler 级总并发上限、Scheduler outcome 捕获包版本与 Collector Descriptor、Agent 规范化插件观测适配、可取消的有界关闭、可选 Agent 插件配置及控制轮询装配均有测试 | 未提供不同 UID 隔离、实际文件/网络/资源限制；回滚关联和隔离状态不持久，Agent 重启会重置；Schema 转换和安全故障注入未完成；不得作为生产隔离使用 |
| P3 采集通道 | 部分完成：插件 Desired/Reported 控制 API、Owner 写入 Handler、签名制品下载和 Agent 对账，以及 `metrics.Batch`、随机幂等 ID、显式有界内存 `BatchQueue`、Agent `SendBatch`、HTTPS 设备鉴权批量 Handler 均已有测试；`cmd/agent` 的批队列默认关闭并要求显式 HTTPS、Token 和四项容量上限；`cmd/server` 可在显式 HTTPS、设备凭据、存储目录和批次上限下挂载 SQLite 批量写入器；路由级测试覆盖持久化及重复确认 | Agent 队列进程重启不保留且容量满时拒绝新样本；Desired State 仍只有内存实现；Server/Agent 目标环境运行链未端到端验收；制品仓库总配额/清理策略未定；Newifi Remote Host 未完成；网络基线测试未执行 |
| P4 数据平台 | 进行中：ADR-019 已确认并实现 CGO SQLite 双库连接、WAL/同步模式校验和事务迁移账本；`metrics.db` 已有原子批次仓储和原始 Descriptor 历史源；`core.db` 已有用户、Session 和账户审计仓储 | 历史路由尚未接入运行时认证和授权；其余核心域持久化、目标设备运行、进程崩溃/断电恢复、并发与 checkpoint、配额及容量验收未完成 |
| P5 流量归属 | 未开始 | 需要确认数据源覆盖、计数域和去重证据后再实现 |
| P6 Web | 部分完成：已有 React Dashboard、登录态前置门、设备列表/指标卡、SSE 实时更新、浏览器临时跨设备指标对比、经确认的磁盘主动刷新、只读网络接口快照页、Owner 账户管理界面，以及按角色开放的 Dashboard 创建/布局编辑/共享草稿发布、发布历史查询、回滚前 Widget/布局预览和新修订回滚 UI；`internal/dashboard` 已定义无执行表达式的 Widget/布局文档、角色门和带版本前置条件的仓储契约，并新增仅供开发/测试的进程内 `MemoryRepository`，覆盖版本 CAS、发布/回滚历史、可见性以及共享变更与审计事件的锁内原子提交；Descriptor Registry、当前快照查询服务和必须显式注入授权器的可选 HTTPS 查询路由均有测试；共享发布要求注入 Viewer 权限校验策略；Widget 可手动读取 Server 最新值和质量状态；另有必须显式配置最大范围和点数上限的存储无关历史查询服务、可选 HTTPS Handler 及授权、范围、返回数据约束测试；Explore 新增用户显式填写设备、指标、时间范围与点数后读取可选历史接口的图表 UI，保留维度序列和质量摘要，不触发采集 | 生产持久化身份、Dashboard 和审计仓储尚未接入 Server，Owner/Dashboard UI 因此尚不能用于当前运行实例；Viewer 发布策略和端到端路由装配、生产历史查询路由/存储接线、Network 流量归属/拓扑、Alerts 页面仍未实现 |
| P7 告警通知 | 部分完成：`internal/alerts` 有版本化规则结构、descriptor/unit/label selector 校验、调用方显式表达式复杂度上限及单轮三值 AND/OR 评估；Dashboard 指标结果有保留质量和维度的显式适配器；stale/missing/unsupported/跨设备/缺少所选维度值产生 Unknown；`MemoryRuleRepository` 提供开发/测试用不可变修订历史、乐观并发控制以及规则 Revision 和管理审计事件的锁内原子提交，`RuleService` 对规则读写实施 Owner 角色检查；可选 Owner HTTP Handler 覆盖 HTTPS Session、Origin、严格 JSON、请求上限、当前列表、CAS 保存、历史查询及以更高新 Revision 回滚；`AdvanceIncident` 提供需显式策略的纯状态转移函数；新增显式容量上限和上一评估时间 CAS 的 `MemoryIncidentRepository`，覆盖幂等重试、并发冲突、容量拒绝及 Revision 隔离；相关模块有 race 单测 | 规则、审计和 Incident 状态都仅在内存，HTTP Handler 未装配到 `cmd/server`，没有敏感操作再认证；阈值、Unknown/Disabled 策略和时间由调用方逐次显式传入，尚无产品默认值；规则删除和旧 Incident 状态迁移、持久化、Incident 通知去重、静默、站内/Web Push 和投递失败隔离仍未决定或实现 |
| P8 发布验收 | 未开始，且不属于本地代码任务 | 需要用户另行授权后制定计划；必须通过生产设备基线与安全红线验收，不能由本地测试替代 |

### 开发约束与停止条件

- 当前可复验检查为根模块 `go test -race ./...`、`go build ./cmd/server ./cmd/agent ./cmd/plugin-pack`、`go vet ./...`、`git diff --check`，独立存储评测模块的单测、race、vet 和构建，以及 `web` 目录下的 `npm test`、`npm run build` 和 `npm run lint`。当前本地 Go 与 Web 检查均通过；这些结果不替代目标设备上的验证。
- Web lint 已无警告；Vite build 仍提示懒加载的 ECharts chunk 为 511 KB（gzip 173 KB），略高于 500 KB 提示线。构建通过，后续可结合实际加载体验评估图表模块拆分。仪表盘 Widget 已增加手动最新值预览，不触发采集；完整历史图表仍未接入。
- 不具备目标主机身份、cgroup 委派和 OS 版本的本地证据时，不实现或宣称具备系统级插件沙箱。
- 未完成故障恢复、checkpoint、配额和容量验收前，不启用未明确配置的后台维护任务，也不将当前本地测试描述为生产验收。
- P5 的流量归属边界、P7 的告警阈值等仍标为待细化的规则，需按决策台账确认；不得为完成路线图自行虚构业务策略。
- P8 设备测试及生产部署不在当前本地工作授权范围内。

## 对 Codex/开发者的约束
优先阅读 docs/README.md 和 00-governance 的决策记录，明确当前代码与规划差距；一次只做一个变更主题；在生产设备修改之前提出待审批计划、验证点与回退方式。禁止以文档设计为由覆盖 AGENTS.md、现有用户改动或未审查配置。

## 已知现状 vs 未来目标
仓库已有 Go Agent 与 Server 初步 API，以及经本地测试的插件、认证和 Web 部分实现。插件生命周期尚不具备系统级隔离，认证/插件管理尚未接入持久化运行配置；Network 页面只是接口快照而不是流量账本，告警系统尚未实现。PRD 与发布说明应按各阶段状态描述，不应将规划目标或未接线能力写作已完成产品能力。
