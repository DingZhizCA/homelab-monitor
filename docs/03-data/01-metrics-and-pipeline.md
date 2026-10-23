# 指标契约、采集流水线与数据质量

> **文档状态**：架构规划与当前实现边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 指标事件主线
Collector → Plugin Host → Agent/Remote Executor → Ingestion → Normalization → Time-Series Store → Aggregator / Query / Alert。原始观测保留源设备、采集位置、测量时间和可解释的计数口径；Web 查询绝不直接催促被监控设备额外采样。

## Metric Envelope 最小字段
| 字段 | 描述 |
|---|---|
| schema_version | 指标消息版本 |
| source_id / collector_id | 观测来源与采集器 |
| observed_at / received_at | 测量时间与接收时间 |
| device_identity_hint | 原始 IP/MAC/Agent/Node ID 等候选身份 |
| metric_id / value / unit | 稳定指标 ID、数值和单位 |
| metric_type | Counter / Gauge / State / Event |
| labels | 受限标签集合，禁止高基数字段滥用 |
| scope | interface / station / transport / device / egress 等计数域 |
| quality | valid、partial、estimated、missing、reset、stale |
| source_revision | 采集协议、插件或配置版本 |

## 计数语义
Counter 先计算两个连续有效快照之间的增量，识别设备重启、接口重新关联、计数器回绕与非单调变化；缺失期间不得默认把流量设为零。Gauge 使用时间加权统计时必须对缺口进行标记，不允许把采样频率不同的 Gauge 简单求和。

## 标准化与安全
校验时间漂移、最大批次、标签数量、单位、设备授权、重复消息 ID。对错误或超预算消息明确返回结构化状态，不允许单次恶意插件响应耗尽数据库内存或磁盘。

设备 ID 在 Agent 配置、指标快照、插件对账/控制、批量队列、规范化观测及 Server 查询边界统一要求非空且无首尾空白；身份值保持原样比较，不在边界静默修剪，以免同一设备出现多个文本别名。

## 汇总与查询
基础采样按时间窗口聚合：Counter 的有效增量求和、网络速率可由字节增量与有效时间计算；CPU/内存保存均值/最大/最小；状态保存变迁；事件按事件时间索引。历史聚合不得把未知标记抹掉。

## 数据质量模型
所有设备级流量必须能够说明观测点、源身份、覆盖起止、未知比例及可能遗漏的路径。历史图表明示数据时区、粒度及状态。失败、stale 和不支持应有独立状态，不能统一显示为 0。

当前实现使用 `MetricEnvelope` v1 接收 Agent 快照；`internal/dashboard` 的内建 Descriptor Registry 只映射现有 CPU、内存、系统负载、磁盘容量和接口速率字段。可选 `/api/v1/dashboard/metrics/descriptors` 与 `/api/v1/dashboard/metrics/latest` HTTP Handler 读取 Server 当前快照，授权发生在快照读取之前。缺少磁盘容量或接口样本会保留为 `missing`，过期设备结果标记为 `stale`；查询只读最新快照，不启动设备采样。`internal/dashboard.HistoryService` 与 `/api/v1/dashboard/metrics/history` HTTPS Handler 提供存储无关的历史查询边界：请求先校验显式配置的范围与点数预算并完成授权，再访问历史源；返回点必须属于请求设备、Descriptor 和半开时间范围。`storage.MetricHistorySource` 可读取原始快照，或按显式年龄窗口依次拼接日、小时、分钟和原始层，并复用同一 Descriptor Registry。聚合点返回桶内有效数值均值并标记 `estimated`；没有有效值的已存在序列保持 `missing`。ADR-020 允许有效 Owner 与 Viewer Session 读取这些现有内建指标。`cmd/server` 在完整显式配置 HTTPS、存储、Origin、Session、限速与历史预算时装配当前值和历史路由；查询不触发采样。更细粒度 ACL 仍未实施。

插件侧使用独立 `metrics.PluginObservation` schema v1 与 Agent `NormalizePluginObservation` 适配器。适配器从已验证的 Collector Descriptor 和 CollectResult 生成带设备、插件版本、Collector、实际观测时间、类型、单位、质量、标签和值的规范化消息，并复制 RPC 数据；它不改变 `MetricEnvelope` v1。Server 已提供独立 `metrics.PluginBatch` 和可选 `POST /api/v1/plugin-metrics/batch`：批次绑定单一设备，显式限制观测数、总样本数和请求字节数，以调用方持久保存的 `batch_id` 幂等重试。设备 HTTPS Token 校验后，批次摘要、接收时间和完整观测在 `metrics.db` 原子提交，复用容量保护；相同 ID 不同内容返回冲突。独立 Collection Policy 控制面已持久化显式周期、超时和 JSON 配置，并通过插件 Revision 防止配置跨版本静默复用。Agent 已以独立显式 tick 读取缓存的 Collection Policy，按策略周期选择稳定顺序的到期任务，并限制策略总数、单轮任务数和跨批次并发；每个 RPC 使用策略自己的超时；Policy 或 Plugin Revision 首次出现时先执行无副作用配置校验，校验成功只对该双 Revision 缓存，之后才执行采集。失败或目标暂不可用同样消耗一个策略周期，避免 tick 变成高频重试。成功结果进入独立原子持久队列，队列同时限制观测数、总样本数和编码字节，单批另限制观测数、样本数和请求字节；发送失败及重启都保留 pending ID。Server 插件历史以设备、插件 ID、插件版本、Collector 和 Metric 的结构化组合选择数据；时间范围、返回点数和原始观测扫描数均受显式预算限制。可选类型化聚合在 UTC 分钟、小时、日桶中分别保存 Gauge 的 mean/min/max/last、Counter 的 delta/reset、State 的 last/transition count 和 Event count；版本、类型、单位与标签都属于聚合键。乱序观测在原始窗口内按桶重算，超出精确窗口或扫描预算时事务失败并保留待处理项。历史点使用 `aggregation_kind`、`resolution_seconds` 及独立 `summary` 字段区分原始值与聚合摘要。插件 Descriptor 发现尚未实现。

规范化消息在进入后续适配器前会再次检查稳定的插件/Collector/Metric/标签标识、受支持的类型与质量、UTF-8 标签及 JSON 值，并用完整标签键值组合区分同一指标的多个序列。

批量上报新增 `metrics.Batch` 协议、`metrics.NewBatchID` 随机幂等键生成器、Agent `BatchQueue`/`SendBatch` 和可选 `POST /api/v1/metrics/batch` Handler。调用方必须将生成的 `batch_id` 与待发送批次一同保存，并在重试时复用；它只用于幂等识别，不代替设备鉴权。`BatchQueue` 的内存实现保留给单元测试；Agent 运行时使用 `OpenBatchQueue`，除快照条数容量、编码后快照总字节容量、单批条数和 JSON 请求体上限外，还要求显式绝对私有状态目录；`SendPending` 在一次显式调用中发送一个待发批次，只有收到不超过 4096 字节且恰含一个有效 JSON 对象、并确认接收条数相符的响应才确认队列内容，失败时保留相同 ID 以便调用方重试。队列不创建定时器或后台发送。Append、首次生成 pending ID 和 Acknowledge 都先将版本化状态写入 `0600` 临时文件，完成文件同步、原子替换和目录同步后才更新内存；状态带设备 ID SHA-256 绑定和内容校验和。运行时持有目录级排他锁；重启恢复会复验版本、权限、文件大小、JSON 唯一键、校验和、设备身份、快照顺序、容量和 pending 请求体，并清理符合内部命名规则的崩溃临时文件；任何异常都拒绝启动，不静默丢队列。该格式不使用追加日志，稳定数据状态只有一个有界文件，另有一个固定锁文件，提交期间最多再有一个临时文件。容量耗尽或状态提交失败时 Agent 拒绝新快照并按现有发送错误限频记录，不淘汰已提交 pending 批次或改变其幂等 ID。批次还需包含单一设备 ID、严格递增的已验证快照；设备 Bearer Token 必须绑定该设备。Handler 要求注入实现原子追加与 ID 去重的历史写入器，重复相同批次不重复更新当前态或 SSE，不同内容复用 ID 返回冲突。乱序批次仍可进入历史写入器，但只有采集时间严格晚于当前状态时才更新最新快照和广播 SSE，避免重放旧批次回退当前视图。`MemoryMetricBatchWriter` 继续作为有显式容量限制的开发/测试实现。生产适配器 `storage.MetricBatchWriter` 在 `metrics.db` 中原子写入批次 SHA-256 摘要、Server 接收时间与完整 `MetricEnvelope` JSON，保存固定精度 UTC 采样时间索引，并在 Server 重启后继续去重；启动恢复只选择每台设备采样时间最新的持久化快照，保留接收时间以正确计算 stale/offline，不重放 SSE 或刷新控制。旧库迁移无法还原真实接收时间时使用批次最后采样时间。启用聚合时，构造器原子启用写入触发器并回填既有快照到持久化待处理队列；每轮按显式条数和超时读取队列，将 Registry 中所有内建 Descriptor 同时 UPSERT 到 UTC 分钟、小时、日桶，并在同一事务删除已完成队列项。原始清理跳过仍在队列中的快照，因此失败或积压不会静默丢失聚合来源；代价是积压可触发既有容量背压。保留任务分别按显式递增窗口和单轮删除上限清理四层数据及更长保留的幂等凭据。`CapacityManager` 在新批次前检查逻辑页占用与文件系统空间，在硬状态执行一次有界过期清理和 PASSIVE checkpoint，仍不安全时返回 `METRIC_STORAGE_FULL`/HTTP 507。每连接 `max_page_count` 限制主库增长。独立物理维护对两库执行阈值触发的 TRUNCATE checkpoint 与页数受限的 incremental vacuum；旧库转换必须显式在 HTTP 监听前完成。Agent 主循环只有在 HTTPS、设备 Token、绝对状态目录和四项容量上限完整时启用持久化批队列；Server 只有在 HTTPS、设备凭据、存储、批次及全部维护参数完整时装配持久化路由。
