# Agent—Server 通信与配置一致性

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 传输建议（本轮按推荐纳入）
HTTPS + Agent 主动轮询。数据平面使用限额批量 POST；控制平面提供期望状态查询及实际状态确认；制品采用按需认证下载。插件 ↔ Plugin Host 使用已经明确确认的 gRPC over Unix Socket，二者是不同协议层。

## 控制平面对象

插件包 Desired State 只管理制品启停、版本和回滚。Collection Policy 独立按 device/plugin/collector 修订，显式携带启用状态、绑定的插件 Desired Revision、采集周期、单次超时及最多 64 KiB 的 JSON 配置。启用策略只有在绑定 Revision 当前启用且 Collector 存在于对应签名 Manifest 时才可发布；禁用策略不保留运行参数。Server 启动时必须显式给出每设备策略数量上限，达到上限拒绝新键但允许更新已有键。策略调整不会推进插件包 Revision、解除插件隔离或自动改变其他采集器频率。Agent 的插件观测循环使用独立显式 tick 和总超时，按策略自身周期与超时运行；策略总数、单轮任务数和总并发受本地硬限制。到期尝试失败或目标不可用也记录本次尝试时间，不会按 tick 加速重试。规范化成功结果先写入独立有界原子队列，再以固定 pending ID 发送到插件观测批次端点。

| 字段 | 意义 |
|---|---|
| desired_revision | Server 期望配置单调版本 |
| observed_revision | Agent 已完成检查的版本 |
| applied_revision | 已成功激活的有效版本 |
| desired_hash | 规范化配置内容摘要 |
| applied_hash | 本地已生效配置摘要 |
| refusal_reason | 本地兼容性、资源或权限拒绝原因 |
| last_reconciled_at | 最近实际对账时刻 |

## 对账算法
1. Agent 以独立设备身份与 Server 建立 TLS 连接，读取适用于自己的期望配置和版本。
2. 未变化时采用条件请求或轻量应答；有变化时先校验目标设备、协议、策略、签名、磁盘空间、资源预算。
3. 对插件升级执行下载、校验、预备、激活、健康观察、提交或回滚；对不兼容配置拒绝执行。
4. Agent 回报 `observed_revision` 与 `applied_revision`，Server 保留不一致状态和审计，不能将发送成功当作生效成功。
5. 期望状态重复出现应幂等，不重复下载或重启插件；旧响应不能覆盖新版本。
6. Agent 只为与实际 Applied Revision 相同的已启用 Collection Policy 构建采集任务；不匹配的策略保持未应用，不能猜测迁移配置。

## 通道职责
| 通道 | 请求语义 | 保护 |
|---|---|---|
| Telemetry | 指标批量、心跳 | 独立容量、拒绝无限缓存 |
| Control | 期望状态与上报结果 | 版本校验、审计、重放防护 |
| Artifact | 制品下载与校验 | 哈希、签名、限速、断点策略待定 |

指标兼容接口为 `POST /api/v1/metrics`，接收一个 MetricEnvelope。新增 `POST /api/v1/metrics/batch` 接收 `{batch_id, device_id, snapshots}`，要求同设备、样本时间严格递增；调用方显式配置最大快照数和请求体字节数。路由必须绑定 HTTPS 设备 Token，并注入支持原子追加和按 batch ID 幂等去重的历史写入器。Agent 有显式快照条数、队列字节数、批次条数及请求体大小限制的 `BatchQueue` 和 `SendBatch` API，`SendPending` 按调用方触发单次发送，只有收到有效接收响应才确认队列内容；pending 批次可在确认前以相同 ID 重试。`cmd/agent` 已提供默认关闭的 `metrics_batch` 装配，只有 HTTPS、设备 Token、绝对私有状态目录和全部容量上限显式有效时才启用。运行时队列以带设备身份摘要和校验和的原子状态快照保存所有未确认快照及 pending ID；重启严格复验并恢复，目录同时只允许一个进程持锁，损坏或并发打开时拒绝启动。状态文件不采用追加日志，稳定数据文件和锁文件数量固定，数据大小受队列边界限制，崩溃临时文件会在启动时清理。队列不创建独立采样或发送定时器，队列满或持久化失败会拒绝新样本。Server 批量 Handler 在并发乱序上报时仅将采集时间晚于现有当前态的样本发布为最新状态。`cmd/server` 现可在同时显式配置 HTTPS、设备凭据、私有 SSD 存储目录、批次限制及存储维护/容量参数时挂载该路由；SQLite 写入器将批次摘要、Server 接收时间与完整快照在 `metrics.db` 的同一事务中提交，并在重启后继续按 batch ID 去重。新批次写入前检查数据库逻辑占用和文件系统保留空间；硬阈值触发一次有界过期清理和 PASSIVE checkpoint，仍不安全时返回 HTTP 507，Agent 不确认 pending 批次。重复且摘要一致的批次不增加存储，即使处于硬容量状态也可完成确认。启动时 Server 按设备恢复最新的已验证持久化快照，沿用原接收时间计算在线状态，不广播伪造的 SSE 事件或重放设备控制；旧数据库迁移因无法还原历史接收时间，保守使用批次最后采样时间。所有相关参数为空时保持原有单快照兼容接口，不创建数据库。插件观测使用独立 `POST /api/v1/plugin-metrics/batch`，请求为 `{batch_id, device_id, observations}`；Server 分别要求显式观测数、总样本数和请求体字节上限，并沿用 HTTPS 设备 Token、批次摘要幂等、SQLite 原子写入、容量检查和 HTTP 507 语义。该入口不更新内建当前态、SSE 或磁盘刷新控制。认证用户可通过独立 `/api/v1/dashboard/plugin-metrics/history` 按 device/plugin/version/collector/metric 精确读取原始历史；版本不得省略，查询同时受时间范围、返回点数和扫描观测数预算约束。

## 插件状态 API 边界
Server 提供以下 Agent 插件状态 Handler：

| 方法与路径 | 语义 |
|---|---|
| `GET /api/v1/agent/plugins/desired?device_id=...` | 返回该设备的期望插件状态快照 |
| `POST /api/v1/agent/plugins/reported` | 接收一个设备的批量已观察/已应用状态 |

期望状态响应为 `{ "device_id": "...", "states": [DesiredState, ...] }`；上报请求为 `{ "device_id": "...", "states": [ReportedState, ...] }`，成功返回 HTTP 202 和接收条目数。两条路由都要求 HTTPS 和绑定到请求 `device_id` 的 Bearer Token。批量上报在版本校验、设备范围校验全部通过后一次提交；过期状态拒绝整批。Handler 依赖窄化仓储接口；`cmd/server` 只有在 SQLite、人类认证、设备认证、私有制品仓库、单包/总量容量边界及有界清理策略全部显式配置时才挂载持久化 Desired/Reported、审计和制品路由。Server 导入超限时拒绝新包；维护任务只清理超过宽限期且未被启用 Desired 引用的完整包，并与 Owner 发布互斥。Agent 插件控制默认关闭，启用时使用独立轮询和操作截止时间，并要求本机信任根、grant、私有 staging/runtime/state 目录、制品上限及本地状态条目/字节上限。Agent 以带设备身份摘要和校验和的原子状态文件保留 observed intent、当前/前序包以及退避/隔离决策；重启不会仅凭本地记录启动旧进程，首次拉取 Server Desired 后才复验暂存包并恢复。同一 Revision 的隔离跨重启保留，新 Revision 才解除。制品下载由独立认证路由完成；激活和回滚仍属于 Agent 本地行为。

## 设备认证建议
TLS 校验 Server；各 Agent 独立设备级令牌（服务器只存校验凭据，scope 绑定 Device ID），预留未来 mTLS 和 CA 轮换。首次注册采用一次性批准及最小范围 bootstrap 凭据。令牌不与用户 Session 或 Newifi SSH 密钥共享。mTLS 是否首版替代 Token 保留实施前安全复核，不影响数据模型。

## 故障策略
Server 不可达时不修改插件运行状态，不无限重试；上报缓存有界，不做离线历史补录；Server 恢复后对账并重检当前数据有效时间。网络通道绝不提供任意 Shell/脚本/端口转发能力。
