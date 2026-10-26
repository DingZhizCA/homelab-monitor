# 存储、分层聚合与配额架构

> **文档状态**：分层聚合运行时已实施；目标容量验收尚未执行
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 已确认存储方案
首版在 Alienware 本地 SSD 上使用 `mattn/go-sqlite3` 与 SQLite WAL。控制面元数据存入 `core.db`，高频指标存入 `metrics.db`；`core.db` 使用 `synchronous=FULL`，`metrics.db` 使用 `synchronous=NORMAL`。业务模块继续通过 Storage Repository 访问数据，不引入独立时序服务、Redis 或消息队列。SQLite 的单写入事务限制仍需通过真实 schema、批量写入和目标环境实测验证。

两个数据库文件必须位于同一个显式配置的私有目录中。运行时负责校验 WAL、同步模式、外键开关和每个连接的 `wal_autocheckpoint=0`，不静默退回其他模式。新数据库在创建首个表前设置 `auto_vacuum=INCREMENTAL`。启用存储必须显式给出 PASSIVE checkpoint 与物理空间回收各自的周期、阈值、工作上限和单次超时；任何一项缺失或非正值都拒绝启动。双库之间不提供原子事务；批次身份和对应指标必须同存于 `metrics.db`，需要审计的控制面变更及其审计事件必须同存于 `core.db`。

根模块故障测试使用独立子进程提交账户、插件 Desired State/审计和指标批次，在两个 WAL 仍打开且非空时直接终止进程，再通过正式 `storage.Open` 重开并执行 `PRAGMA quick_check`，验证控制状态、当前指标和批次幂等性可恢复。该测试证明当前开发机上的已提交事务可经进程崩溃恢复；它不模拟断电、SSD 缓存行为或目标主机文件系统。

当前 `metrics.db` schema 保存内建批次 ID、设备 ID、内容 SHA-256、快照数，以及按设备和固定精度 UTC 采样时间索引的完整 `MetricEnvelope` JSON。独立插件批次表保存相同语义的幂等摘要、接收时间与观测数；插件观测表保留包版本、Collector、观测时间及完整 JSON，并按设备时间和来源时间建索引。内建与插件聚合各有持久化待处理队列、写入触发器和分钟/小时/日聚合表。内建聚合键包含粒度、UTC 桶起点、设备、Descriptor 与规范化维度 JSON；插件键额外保留插件版本、Collector、Metric、类型、单位和规范化标签，摘要 JSON 依类型保存 Gauge、Counter、State 或 Event 语义。原始历史适配器通过索引流式读取范围并在达到请求点数时停止，分层适配器按年龄窗口查询聚合表或原始表。全局采样时间、批次接收时间和各层桶起点索引支持有界保留清理。

`core.db` 已有用户、服务端 Session、插件 Desired/Reported State、Dashboard/不可变修订、告警规则不可变修订、Incident 当前状态、跨 Revision 不可变归档和通用审计事件 schema。认证仓储将首次 Owner、Owner 创建账户及角色/启用状态变更与对应审计事件放在同一事务，原子复核当前 Owner 和最后 Owner 约束；Session 只保存 SHA-256 Token 摘要。Dashboard 仓储在事务内复核当前 Actor 启用状态和角色，以 CAS 更新草稿或发布指针；共享创建、草稿保存、发布和回滚与审计事件原子提交。告警规则仓储同样复核当前 Owner，以 `(device_id, rule_id)` 隔离历史并原子推进当前 Revision；相同内容重试幂等，回滚写入更高新 Revision。Incident 仓储保存每条设备规则的当前转换状态，要求显式最大条数，以最后评估时间执行 CAS，并拒绝隐式跨 Rule Revision 迁移。显式激活事务再次复核当前 Rule Revision，把旧状态按 `(device_id, rule_id, rule_revision)` 写入容量受限的 `incident_revision_archives`，以 Revision 和最后评估时间删除 current，并在同一事务写入 Owner 审计；容量或 CAS 冲突不会部分归档。有界评估运行时从规则仓储按稳定游标和本轮额度分页读取当前 Revision，跨轮续接并循环回绕，再用 Server Current State 推进 Incident；单条失败不影响同轮其他规则。Session 过期和 Incident 归档保留周期仍未确定；当前时间保留任务只处理 `metrics.db`，不会清理 Session 或其他控制面数据。

## 数据域
| 数据域 | 主要数据 | 保护重点 |
|---|---|---|
| Core Metadata | 用户、Session、Device、Identity、Plugin、Desired State | 不受指标库清理影响 |
| Time Series | 原始测量、分钟/小时/日聚合 | 配额、自动过期、分层查询 |
| Flow Ledger | 设备方向、出站、Exit Node 账本及覆盖质量 | 长期一致性、去重 |
| Events / Alerts | 设备事件、Incident、规则版本与审计 | 不因插件卸载失效 |
| Diagnostics | 按需临时连接详情 | 单独配额、TTL、敏感权限 |

## 可配置保留架构
采样频率和上报频率属于 Collector/Agent；存储粒度、保留期限和硬配额属于存储策略。当前 C1 运行时要求显式配置原始、分钟、小时、日四个严格递增窗口、较长的批次幂等凭据窗口、清理周期、每轮删除上限和单次超时。清理按各自桶时间删除过期聚合行；原始快照只有在超过原始窗口且不存在待聚合队列引用时才可删除，随后按 `received_at` 删除超过凭据窗口且已经没有快照的批次身份。删除使用单事务和每类独立条数上限，数据库繁忙或超时时整轮回滚并等待下一周期，不改变 Agent 采样频率。

`MetricAggregationManager` 启用时会在事务中打开入队开关并把既有原始快照补入队列；以后每个新快照由数据库触发器登记。单轮读取显式上限的队列项，复验索引和 Envelope，一次提取所有内建 Descriptor，并在同一事务 UPSERT 三个 UTC 粒度后确认队列项。`PluginMetricAggregationManager` 使用独立开关和队列；单轮待处理观测数、为乱序重算扫描的原始观测总数及事务超时均有显式上限。它按完整来源和 Metric 处理受影响桶：Gauge 生成 mean/min/max/last，Counter 按时间计算 delta 并识别显式或数值回退 reset，State 比较规范 JSON 值计算 transition count，Event 计算有效事件数；missing 与 stale 不进入摘要。原始窗口内的桶可重算，跨越窗口的长桶按时间增量合并；运行中途首次创建而缺少桶前段的长桶标记 `partial`。乱序数据只有在聚合首样本仍位于原始窗口时才能重算，否则明确失败并保留队列。两种聚合失败都会整轮回滚；原始清理跳过对应待处理数据，积压最终由容量保护形成背压。保留任务对原始观测、三层插件聚合和更长保留的插件批次凭据分别执行有界删除。已观测 Descriptor Catalog 与观测批次同事务登记，迁移会从既有原始观测回填；同一设备、插件版本、Collector 和 Metric 出现类型、单位或 label keys 冲突时，迁移或新批次明确失败。目录保留首次和最近观测时间，原始数据过期后仍可用于构造历史选择器。

分层历史源以查询时刻和四个显式窗口划分互不重叠的范围，从旧到新读取日、小时、分钟和原始数据，并共用请求点数上限。小时和分钟切换点向新层粒度上取整，避免保留边界形成空洞。插件历史接口对每个点返回 `resolution_seconds` 与 `aggregation_kind`；原始点使用 `value`，聚合点使用 `summary`，避免把 Counter 增量或 Event 数量伪装成原始值。内建历史接口仍以 `estimated` 质量表示聚合值，尚未增加精确粒度字段。

## 配额清理策略（已确认）
Server 要求显式给出 `core.db` 页分配硬上限、指标逻辑软阈值、`metrics.db` 页分配硬上限和共享文件系统最小保留字节。字节上限按数据库页大小向下对齐；自定义 SQLite 驱动在每个新连接上设置并复核 `max_page_count`，避免连接池或进程重开恢复无限默认值。指标逻辑占用使用 `(page_count - freelist_count) * page_size`，WAL 物理大小单独观测；文件系统判断为可用字节至少覆盖显式保留量与当前请求编码字节。

软阈值只改变可观察状态，周期保留任务继续分批清理过期数据。逻辑硬阈值或文件系统保留空间不足时，写入路径同步执行一轮有界过期清理和 metrics PASSIVE checkpoint，再次测量仍不安全便拒绝整个新批次并返回 HTTP 507；同一保留任务周期内的后续硬状态请求直接拒绝，不重复运行清理和 checkpoint。相同 ID 与摘要的重复批次在容量检查前完成去重，因此可确认已经提交但响应丢失的重试。该机制不删除未过期数据，也不保证精确估算 SQLite 索引和页开销；若页上限先触发 SQLite FULL，写入器会强制执行一次相同的有界恢复并只重试事务一次，再次失败才返回 507。控制库拥有独立硬上限，不受指标清理影响；具体字节值仍需目标设备容量验证。

## SQL 和后台任务
使用批处理插入、适量索引、有界查询、分页和点数上限。每个 SQLite 连接同时复核 auto-checkpoint 关闭状态和已配置页数上限。Server 按显式检查周期检查两个 WAL 文件；文件达到显式字节阈值且自上次成功操作后发生变化时，执行有单次超时的 `PRAGMA wal_checkpoint(PASSIVE)`。该模式不等待活跃 Reader/Writer，进程正常退出前也对两库各尝试一次；每次尝试记录数据库、原因、WAL 字节数、busy、日志帧、已 checkpoint 帧、耗时和错误。PASSIVE 只把可用帧同步回主库，不保证截断物理 WAL 文件；阈值基于文件大小，文件保持高水位后，有新写入时会在后续检查周期再次尝试。

独立物理空间任务按更保守的显式周期检查两库。WAL 达到截断阈值时执行有超时的 `PRAGMA wal_checkpoint(TRUNCATE)`；空闲页达到触发值时执行 `PRAGMA incremental_vacuum(N)`，其中 `N` 是显式单库单轮页数上限，随后尝试 TRUNCATE checkpoint 使变更落入主库。繁忙、锁等待或超时只使当前数据库本轮失败，下一数据库仍单独观测；记录回收前后主库分配字节、空闲页、WAL 字节、动作、帧数、耗时和错误。该任务不删除行，只回收保留任务或业务删除已经释放的页，不周期运行全库 VACUUM。

旧数据库若为 `auto_vacuum=NONE`，物理任务拒绝启动，避免把无效回收写成成功。Owner 可显式开启一次性启动迁移并设置独立超时；Server 在监听 HTTP 前检查文件系统可用空间可覆盖当前库文件和既有保留字节，再顺序执行 `PRAGMA auto_vacuum=INCREMENTAL; VACUUM`。这是可能耗时且需要额外临时空间的格式转换，完成后应移除迁移开关。目标设备仍需验证 TRUNCATE 锁等待、每轮页数、SSD 空间变化和上报延迟。

## HDD 约束
监控 DB 和写入工作目录放在系统 SSD，禁止周期性 SMART、磁盘遍历或其他会唤醒休眠 HDD 的任务。需要备份到 HDD 时必须由 Owner 单独审批唤醒策略。

## 演进触发
根据实际时序写入吞吐、查询 p95、保留容量和磁盘压力判断是否需要更换时序存储。必须通过 Repository/Export API 实现迁移，不让 Web Widget 直接依赖某个数据库 SQL 方言。
