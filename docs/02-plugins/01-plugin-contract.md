# Collector 插件接口与 SDK 契约

> **文档状态**：架构规划与当前实现边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 运行时类型
首版只有 native Linux 原生独立进程插件。每包通常一个进程，可在包内包含多个 Collector；安全敏感或高成本插件允许进一步拆分。暂不开发 Go `.so` 动态加载、WASM 双运行时或插件市场。

## Manifest 最小模型
| 字段 | 定义 |
|---|---|
| plugin_id / version | 不可变的全局插件身份和语义版本 |
| publisher / signature | 可信发布者和签名信息 |
| platform | OS / 架构 / ABI 与二进制适配 |
| protocol_range | Plugin API 兼容的协议范围 |
| metric_schema_versions | 可选声明插件可输出的指标模型版本；缺失或空列表表示旧包未声明兼容性 |
| collectors | 包含的 Collector 能力列表 |
| permissions | 需要读取的系统资源、网络终点与特权能力 |
| resource_profile | 候选常驻内存、CPU、超时及启动成本 |
| config_schema | 不含密钥值的受控配置约束 |

## gRPC 本机契约
| 方法 | 语义 | 必须保证 |
|---|---|---|
| Handshake | Host/插件身份、协议能力握手 | 拒绝不兼容版本 |
| Describe | 返回 Collector、Metrics 描述 | 只返回静态能力，不触发采集 |
| ValidateConfig | 无副作用预检 | 禁止通过校验执行采集 |
| Collect | 以指定 Deadline 返回有界结果 | 可取消、限额、完整错误码 |
| Health | 获取插件自身状态 | 不混淆目标设备是否在线 |
| Shutdown | 受控关闭 | 有超时，必要时由 Supervisor 终止 |

## 统一指标描述
Descriptor 包含 metric_id、type(counter/gauge/state/event)、unit、label schema、source_kind、data_quality、默认允许聚合方式和访问权限。每条 MetricSample 携带与 Descriptor 声明完全相同的 label keys；同一 metric_id 可通过不同 label values 表示多个序列，重复的 metric_id 与 labels 组合会被拒绝。插件不得决定用户权限或直接写数据库。`observed_at` 为实际测量时刻，`received_at` 由 Server 记录；`source_id`、`plugin_id`、`collector_version` 用于来源审计。

## 低级能力与特殊场景
插件只应通过最小接口报告事实数据；Agent/Server 统一执行跨设备身份归属、指标汇总和告警判断。Newifi 远程 Collector 可以运行在 Alienware Host，提供目标 Endpoint 的受限查询参数；Newifi 的高权限密钥不能传入任意插件进程。

## 协议演进
重大不兼容使用 API major 版本升级；扩展字段优先采用向后兼容定义；未知字段可忽略但不能猜测未知类型；Metric Schema 单独升级并提供 Adapter。Host 应持续执行 SDK contract tests 和跨版本握手测试。

当前 Manifest 校验器接受 `metric_schema_versions` 中唯一且路径安全的版本标识。该字段省略时仍保持旧版规范 JSON 的字节形式，避免破坏已有签名；这只记录制品声明，不代表 Host 已完成 Schema 协商、转换或业务兼容性判断。

插件 RPC v1 为 MetricSample 增加了可选的 protobuf labels map，并在 HandshakeRequest 增加可选 host_features 字段；这两个字段是 wire-additive，不改变现有无标签插件的消息编码。Host 宣告 `metric_sample_labels` 能力，插件若需要标签值，应在握手时检查该能力并在旧 Host 未宣告时拒绝启动。新 Host 对声明了 `label_keys`、但未为每条样本返回完整标签的旧插件会拒绝采集结果；发布已有此类插件前需升级插件实现并做 SDK 契约测试。此迁移可避免旧 Host 静默丢失新插件的序列维度，协议 major 暂不升级。

公开 Go SDK 位于 `pkg/plugin`：插件作者可实现 transport-independent `Plugin` 接口并调用 `ServeUnix` 在 Supervisor 提供的私有运行目录中启动 gRPC 服务。SDK 仅限制 Unix Socket 权限和 RPC 消息大小，不授予 OS 级能力隔离；插件配置、周期与指标映射仍由后续控制面和 Agent 采集集成确定。
