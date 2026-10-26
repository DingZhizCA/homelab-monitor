# 架构决策台账与批准状态

> **文档状态**：持续实施；各项实际状态见路线文档
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 文档定位
本台账整理截至 2026-10-17 的 Homelab Monitor 方案。优先级：用户明确确认的决策 > 本轮「全部按推荐」授权采用的架构建议 > 仅供实施阶段验证的候选参数。本文不代表当前程序已完成这些能力。

## 已明确确认的架构决策
| ID | 领域 | 决策 | 状态 |
|---|---|---|---|
| ADR-001 | 产品定位 | 可扩展的家庭监控平台，设备、网络、服务、流量、告警一体化 | 已确认 |
| ADR-002 | 中心部署 | Alienware 为唯一长期运行的监控中心；无需 HA 或离线历史补录 | 已确认 |
| ADR-003 | Server | Go 模块化单体，不预先拆微服务 | 已确认 |
| ADR-004 | 设备采集 | N1 和 Alienware 用 Go Agent；Newifi 优先无 Agent 远程读取 | 已确认 |
| ADR-005 | 插件范围 | 第一版完整 Collector 插件管理系统，不是只有静态注册接口 | 已确认 |
| ADR-006 | 插件运行时 | 原生独立进程；gRPC over Unix Socket；每个插件包一个进程为默认 | 已确认 |
| ADR-007 | 插件分发 | Server 管理期望状态，Agent 主动拉取；本地安全校验、拒绝与回滚 | 已确认 |
| ADR-008 | Web 网络边界 | 仅 LAN 和 Tailnet 访问，不直接暴露公网 | 已确认 |
| ADR-009 | 身份与角色 | 应用本地账号统一登录；Owner 与 Viewer | 已确认 |
| ADR-010 | 远程管理 | Newifi 专用受限采集授权，管理员密钥与监控密钥分离 | 已确认 |
| ADR-011 | 历史 | 采样、聚合、保留期限、配额 Web 可配置；自动分层清理 | 已确认 |
| ADR-012 | 流量 | 全局域网设备和 Tailscale 客户端，含 Exit Node；避免重复计数 | 已确认 |
| ADR-013 | 诊断 | 日常轻量汇总；连接明细仅 Owner 按需限时开启 | 已确认 |
| ADR-014 | 设备身份 | 高可信自动关联；模糊情况 Owner 审核，可合并拆分和追踪 | 已确认 |
| ADR-015 | 在线监测 | 默认被动发现；Owner 可以发起限时低成本单设备探测 | 已确认 |
| ADR-016 | 可视化 | 多仪表盘、Widget、用户个性化布局；Owner 发布共享版本 | 已确认 |
| ADR-017 | 告警 | 基础高可信模板默认启用；受限 AND/OR，模板语义变更 Owner 审核 | 已确认 |
| ADR-018 | 通知 | Web 站内通知 + PWA Web Push；Critical/Warning 默认推送 | 已确认 |
| ADR-019 | Server 持久化 | `mattn/go-sqlite3`；Alienware 本地 SSD 上使用 `core.db` 与 `metrics.db` 两个 WAL 文件；`core.db` 使用 `synchronous=FULL`，`metrics.db` 使用 `synchronous=NORMAL` | 2026-10-22 已确认 |
| ADR-020 | 指标读取授权与配置 | Owner 与 Viewer 均可读取现有内建当前/历史指标；Server 认证、限速和历史预算通过无默认值的显式 CLI 参数启用 | 2026-10-22 已确认 |
| ADR-021 | SQLite WAL checkpoint | 关闭每个连接的隐式 auto-checkpoint；Server 采用显式周期、WAL 字节阈值和单次超时驱动 PASSIVE checkpoint，正常退出前再尝试一次并记录结果 | 2026-10-22 已确认 |
| ADR-022 | 原始指标保留与容量保护 | 采用 C1 有界自动清理：原始快照与批次幂等凭据使用两个显式窗口，凭据窗口必须更长；每轮删除量和超时显式配置；`core.db`/`metrics.db` 使用每连接页数硬上限，指标写入检查逻辑软硬阈值和文件系统保留空间，硬阈值清理与 PASSIVE checkpoint 后仍不安全则返回 507 | 2026-10-22 已确认 |
| ADR-023 | 插件制品仓库容量与清理 | Server 制品仓库采用单包、总字节、包数量和扫描条目硬边界；导入达到边界时拒绝，不在写入路径隐式删除；周期维护仅删除超过显式宽限期且未被任何启用 Desired State 引用的完整包，限制单轮数量和超时，并与 Owner Desired 发布互斥 | 2026-10-23 已确认 |
| ADR-024 | SQLite 物理空间回收 | 新数据库创建为 `auto_vacuum=INCREMENTAL`；周期维护按显式 WAL 阈值执行 `TRUNCATE` checkpoint，并按空闲页触发值和单轮页数上限执行 incremental vacuum；每库操作有超时和结构化观测。旧库只允许在显式启动迁移开关、超时和文件系统预留检查下，于 HTTP 监听前执行一次全库 VACUUM 转换 | 2026-10-23 已确认 |
| ADR-025 | 指标分层聚合与查询 | 内建 Descriptor 的原始快照通过持久化待聚合队列一次生成 UTC 分钟、小时、日三层；聚合更新与队列确认同事务，原始清理不得越过未确认项。数值查询返回桶内有效值均值并标记 `estimated`，全缺失桶保持 `missing`；历史源按显式递增的原始/分钟/小时/日窗口拼接单一来源。周期、单轮快照数、超时和各层保留窗口均无默认值 | 2026-10-23 按推荐采用 |
| ADR-026 | Agent 插件暂存容量 | staging 采用单包、总逻辑字节、包数和扫描条目硬边界及跨进程单写者锁；写入前按磁盘实况核算，满额拒绝且不在写入路径隐式淘汰。控制轮询按显式周期、宽限期、单轮删除量和超时清理，仅保护 Reconciler current/previous 后删除完整旧包；保护集、目录结构或扫描异常时停止本轮 | 2026-10-23 按推荐采用 |
| ADR-027 | 插件进程基础资源边界 | Agent 生产装配必须通过独立非特权 `plugin-launcher` 在 `exec` 插件前设置硬 `RLIMIT_NOFILE`、`RLIMIT_NPROC`、`RLIMIT_FSIZE`、`RLIMIT_AS`、`RLIMIT_CPU` 和 `no_new_privs`；launcher 绝对路径与各限制值必须显式配置且无默认值。该层用于无竞态地建立基础进程边界，不宣称提供 cgroup 总量、RSS、不同 UID、文件系统或网络隔离 | 2026-10-23 按推荐采用 |
| ADR-028 | 插件采集策略控制 | Collector 的启用、JSON 配置、采集周期和单次超时使用独立于插件制品 Desired State 的 Collection Policy；按 device/plugin/collector 独立单调修订并原子审计，不提供隐式采样参数。启用策略必须绑定已启用的具体插件 Desired Revision 且 Collector 存在于对应签名 Manifest，避免旧配置静默应用到升级后的 Collector；Server 要求显式每设备策略数量上限，单份 JSON 配置使用 64 KiB 协议硬上限 | 2026-10-23 用户确认采用推荐方案 |
| ADR-029 | 插件观测持久化入口 | 插件观测使用独立于 `MetricEnvelope` 的 `{batch_id, device_id, observations}` 幂等协议和 `/api/v1/plugin-metrics/batch`；请求必须显式限制观测数、总样本数和字节数并绑定 HTTPS 设备凭据。批次摘要、接收时间和完整观测在 `metrics.db` 同一事务提交，复用指标库容量保护；原始观测和凭据分别沿用显式原始/凭据窗口有界清理。当前不将插件数据伪装成内建聚合或查询结果 | 2026-10-23 按推荐实施 |
| ADR-030 | Agent 插件采集调度与队列 | Collection Policy 使用独立显式 tick 扫描到期任务，每轮策略数、任务数和跨批次并发均有硬上限；每个任务使用策略自身超时，Policy 或 Plugin Revision 变化时必须先通过无副作用配置校验。一次到期尝试无论成功、失败或目标不可用都推进本地尝试时间，禁止按 tick 高频重试。成功观测进入独立原子快照队列，队列观测数、总样本数、编码字节及单批三项边界均显式配置；批次 ID 在确认前和重启后保持不变 | 2026-10-23 按推荐实施 |
| ADR-031 | 插件原始历史查询 | 插件历史使用独立认证 API，并以 device/plugin/version/collector/metric 结构化选择器查询，插件版本必填，禁止将不同版本自动合并。请求复用显式时间范围和返回点数预算，并增加底层原始观测扫描数硬上限；稀疏查询达到扫描上限时明确失败，不返回伪完整的截断结果 | 2026-10-23 按推荐实施 |
| ADR-032 | 插件类型化分层聚合 | 插件聚合键保留 device/plugin/version/collector/metric/type/unit/labels；Gauge 保存 mean/min/max/last，Counter 保存按时间排序的 delta/first/last/reset count，State 保存 last/transition count，Event 保存 event count。每个返回点显式区分 `raw` 与聚合种类并携带 `resolution_seconds`；聚合器在原始窗口内按桶重算以正确处理乱序，长桶按时间增量合并，运行中途建立且缺少桶前段的摘要标记 `partial`。没有完整重算来源的过旧乱序数据明确失败而不静默改写。周期、队列数、扫描数、超时和四层保留窗口均须显式配置 | 2026-10-23 用户选择 A 后实施 |
| ADR-033 | 已观测插件 Descriptor Catalog | 当前插件 Descriptor 只有 metric/type/unit/label keys，Server 可从 Agent 已按运行时 Descriptor 验证的规范化观测无损重建。目录以 device/plugin/version/collector/metric 为键，与观测同事务登记；同一键的类型、单位或 label keys 漂移使整批回滚。认证目录只展示实际出现过数据的 Schema，并受显式每设备条数上限约束；首次成功观测前不可见，将来增加名称、说明等非观测字段时再引入独立发布协议 | 2026-10-23 按推荐实施 |
| ADR-034 | Web 插件历史浏览 | Explore 以已观测 Descriptor Catalog 构造 device/plugin/version/collector/metric 精确选择器，不跨版本合并，也不触发 Collector。结果按 `aggregation_kind` 映射类型化摘要：Gauge 绘制 mean、Counter 绘制 delta、Event 绘制 event count，State 和非数值原始值仅展示明细；页面必须显示 `resolution_seconds`、质量（含 `partial`）和类型化摘要 | 2026-10-25 按推荐实施 |
| ADR-035 | Dashboard 插件指标引用 | 现有内建 Widget 的 `descriptor_id` 与 `device_id` 保持兼容；插件 Widget 使用独立结构化 `plugin_metric` 保存 plugin/version/collector/metric，禁止与内建引用混用。共享发布按设备精确验证已观测目录，且仅在插件目录与历史查询面同时启用时允许插件引用；个人与草稿仍可保存结构合法的引用，实际查询继续逐次鉴权。插件 Widget 历史预览必须由用户显式填写起止时间和点数上限，不提供隐式范围 | 2026-10-25 按推荐实施 |
| ADR-036 | 内建历史粒度元数据 | 内建历史点以向后兼容的新增字段显式返回 `resolution_seconds` 和 `aggregation_kind`；原始点为 `0/raw`，当前数值聚合点为正分辨率与 `gauge_mean`。History Service 校验两者组合，Web 按实际返回值展示混合分层结果 | 2026-10-25 按推荐实施 |
| ADR-037 | 当前 Incident 查询边界 | 告警评估显式启用时挂载只读 `/api/v1/alerts/incidents`；Owner 与 Viewer 通过 HTTPS Session 按唯一 `device_id` 读取评估器同一 SQLite 仓储中的当前转换状态。Web 只在进入告警页、切换设备或用户手动刷新时查询。查询不推进状态、不触发采集，也不把当前状态伪装成 Incident 历史；规则写入仍保持 Owner 专属 | 2026-10-25 按推荐实施 |
| ADR-038 | Web 告警规则编辑边界 | 首批 Owner 编辑器只创建和修订可无损回填的单指标规则；级别、启用状态、Descriptor、运算符、阈值和全部维度都由 Owner 明确填写，不设业务默认值。Revision 按当前版本递增并携带 CAS；已有 AND/OR 表达式只读展示，防止被简化编辑器静默覆盖。所有规则均可查看不可变历史；回滚由 Owner 明确选择目标，Server 复制完整历史内容并创建下一个 Revision | 2026-10-25 按推荐实施 |
| ADR-039 | Rule Revision 的 Incident 激活 | Rule Revision 变化不继承旧 Pending/Firing/Recovering 时间，也不自动关闭旧状态；引擎继续拒绝隐式跨版本推进。Owner 通过专用操作显式激活当前 Rule Revision，仓储原子复核 Owner、Rule Revision 和 Incident CAS，将旧 current 写入有界不可变 `superseded` 归档、删除 current 并提交审计；下一轮从干净状态评估。归档容量必须显式配置，容量满时保留旧状态并拒绝激活 | 2026-10-26 用户确认推荐方案 |
| ADR-040 | Superseded Incident 归档查询 | Owner 与 Viewer 可通过只读 API 查询单一设备和规则的 superseded Revision；请求必须显式提供正整数条数上限且不得超过配置的归档总容量，结果从新到旧返回。Web 不预填上限、不自动读取，查询不推进评估、不触发采集 | 2026-10-26 按推荐实施 |
| ADR-041 | Incident 历史保留 | superseded 归档容量继续作为硬边界；可选周期清理必须同时显式配置周期、保留时长、每集合单轮删除量和超时，不提供产品默认值。清理分别按 `superseded_at` 和 `occurred_at` 最旧优先删除严格早于截止时间的归档与状态转换，不触及 current、规则历史或审计事件；部分配置在启动时失败 | 2026-10-26 按推荐实施 |
| ADR-042 | Incident 状态转换事件 | 只为状态真正变化的评估写入不可变事件，不为状态不变的每轮评估重复写历史。事件保存前后状态、结果状态快照、Rule Revision、UTC 时间和确定性 ID，并与 current CAS 更新在同一事务提交；容量满时两者一起失败。转换历史需以显式总容量开启，并强制依赖完整历史保留配置；未启用时保持现有 current 行为 | 2026-10-26 按推荐方案 A 实施 |
| ADR-043 | Incident 转换历史查询 | 仅在转换历史显式启用时挂载只读 API；Owner 与 Viewer 必须提供唯一设备、规则和正整数条数上限，且不得超过转换历史容量。结果按发生时间与事件 ID 从新到旧排序，空结果稳定返回空数组；查询不评估规则、不触发采集 | 2026-10-26 按推荐实施 |

## 根据「全部按推荐」授权纳入基线、尚未逐项单独确认
| ID | 领域 | 推荐基线 | 状态 |
|---|---|---|---|
| REC-001 | Agent ↔ Server | HTTPS，Agent 主动轮询期望状态、批量上报指标、按需下载插件 | 本轮按推荐采用 |
| REC-002 | 状态管理 | desired / observed / applied revision 声明式对账、拒绝与回滚 | 本轮按推荐采用 |
| REC-003 | Agent 认证 | TLS + 独立设备凭据（初始采用作用域受限设备 Token，预留 mTLS） | 推荐基线，实施前安全复核 |
| REC-004 | 持久化 | SQLite WAL；元数据与高频数据使用独立数据库文件，容量驱动升级 | 驱动、文件拆分及同步模式已转为 ADR-019；容量仍需压测确认 |
| REC-005 | Web API | 受控 HTTP JSON API；实时订阅优先 SSE，命令操作仍经 HTTP API | 推荐基线，实施前验证 |
| REC-006 | 插件发布 | 可信签名制品、哈希校验、平台兼容检查、本地运行权限约束 | 推荐基线 |
| REC-007 | 插件制品传输 | Agent 使用设备凭据通过 HTTPS 拉取；Manifest、签名和二进制作为 multipart/form-data 独立部分传输，摘要必须与 Desired State 匹配 | 本地组件和测试已采用；`cmd` 装配与目标设备验收未完成 |

## 未批准修改的对象
OpenWrt 网络配置、N1 sing-box、Tailscale、防火墙、生产 systemd、Alienware 服务与磁盘配置均未因本套文档而被修改。代码和文档的实际落地状态以实施路线及 Git 历史为准。

## 架构与业务的分界
架构项在本套文档明确边界、协议、模块、责任及故障策略；告警阈值、具体采样频率、保留天数、默认 Widget、PWA 细节、准确率阈值等仅列为候选值或延后决议，不视为正式定稿。

## 冲突处理与维护
架构冲突先修订本台账并新增 ADR，不能直接在代码里改变已确认约束。建议架构变更记录包含：问题、候选方案、取舍、批准人、时间、影响、回滚方式。
