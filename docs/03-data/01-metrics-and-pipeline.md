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

当前实现使用 `MetricEnvelope` v1 接收 Agent 快照；`internal/dashboard` 的内建 Descriptor Registry 只映射现有 CPU、内存、系统负载、磁盘容量和接口速率字段。可选 `/api/v1/dashboard/metrics/descriptors` 与 `/api/v1/dashboard/metrics/latest` HTTP Handler 读取 Server 当前快照，必须注入显式 `MetricAuthorizer`，授权发生在快照读取之前。缺少磁盘容量或接口样本会保留为 `missing`，过期设备结果标记为 `stale`；查询只读最新快照，不启动设备采样。`internal/dashboard.HistoryService` 与可选 `/api/v1/dashboard/metrics/history` HTTPS Handler 提供存储无关的历史查询边界：构造时必须显式传入最大时间范围与点数预算，请求先校验范围/点数并完成字段授权，再访问注入的历史源；返回点必须属于请求设备、Descriptor 和半开时间范围，维度须匹配 Descriptor，并受点数上限约束。`storage.MetricHistorySource` 已能按设备和时间索引流式读取 `metrics.db` 的原始快照，复用同一 Descriptor Registry 映射字段，在达到请求点数时停止读取；该查询不触发采样。Explore 页面现可在用户明确填写设备、Descriptor、时间范围和点数后调用该历史路由并渲染带维度序列的趋势图。`cmd/server` 尚未装配用户认证、字段授权和历史查询预算，因此该历史路由仍未在可执行文件启用；时间聚合口径、留存和 Viewer 字段策略仍待业务决策。

插件侧当前增加独立 `metrics.PluginObservation` schema v1 与 Agent `NormalizePluginObservation` 适配器。适配器从已验证的 Collector Descriptor 和 CollectResult 生成带设备、插件版本、Collector、实际观测时间、类型、单位、质量、标签和值的规范化消息，并复制 RPC 数据；它不改变 `MetricEnvelope` v1、不触发采样、不排队或上报。通用 Server ingestion、幂等键、存储和历史查询映射仍未实现，待数据保留和存储策略确定后再接入。

规范化消息在进入后续适配器前会再次检查稳定的插件/Collector/Metric/标签标识、受支持的类型与质量、UTF-8 标签及 JSON 值，并用完整标签键值组合区分同一指标的多个序列。

批量上报新增 `metrics.Batch` 协议、`metrics.NewBatchID` 随机幂等键生成器、Agent `BatchQueue`/`SendBatch` 和可选 `POST /api/v1/metrics/batch` Handler。调用方必须将生成的 `batch_id` 与待发送批次一同保存，并在重试时复用；它只用于幂等识别，不代替设备鉴权。`BatchQueue` 仅驻留内存，构造时要求显式设置快照条数容量、编码后快照总字节容量、单批条数和 JSON 请求体上限；`SendPending` 在一次显式调用中发送一个待发批次，只有收到不超过 4096 字节且恰含一个有效 JSON 对象、并确认接收条数相符的响应才确认队列内容，失败时保留相同 ID 以便调用方重试。队列不创建定时器或后台发送。进程退出会丢失尚未确认的快照；容量耗尽时 Agent 拒绝新快照并按现有发送错误限频记录，不淘汰 pending 批次或改变其幂等 ID。批次还需包含单一设备 ID、严格递增的已验证快照；设备 Bearer Token 必须绑定该设备。Handler 要求注入实现原子追加与 ID 去重的历史写入器，重复相同批次不重复更新当前态或 SSE，不同内容复用 ID 返回冲突。乱序批次仍可进入历史写入器，但只有采集时间严格晚于当前状态时才更新最新快照和广播 SSE，避免重放旧批次回退当前视图。`MemoryMetricBatchWriter` 继续作为有显式容量限制的开发/测试实现。生产适配器 `storage.MetricBatchWriter` 在 `metrics.db` 中原子写入批次 SHA-256 摘要与完整 `MetricEnvelope` JSON，保存固定精度 UTC 采样时间索引，并在 Server 重启后继续去重；它尚未实施保留、配额、聚合或历史 Descriptor 映射。Agent 主循环已支持显式 `metrics_batch.enabled`：只有 HTTPS、设备 Token 和四项容量上限都配置后才以每轮采样调用批队列；默认继续使用兼容单快照接口。正常链路会立即发送当前样本，短暂失败期间积累的后续样本可在 pending 批次确认后合并发送。Server 只有在 HTTPS、设备凭据、存储目录和两个批次上限全部显式配置时才装配持久化批量路由；默认运行方式不创建数据库。
