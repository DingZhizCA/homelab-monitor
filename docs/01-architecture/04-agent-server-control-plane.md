# Agent—Server 通信与配置一致性

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 传输建议（本轮按推荐纳入）
HTTPS + Agent 主动轮询。数据平面使用限额批量 POST；控制平面提供期望状态查询及实际状态确认；制品采用按需认证下载。插件 ↔ Plugin Host 使用已经明确确认的 gRPC over Unix Socket，二者是不同协议层。

## 控制平面对象
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

## 通道职责
| 通道 | 请求语义 | 保护 |
|---|---|---|
| Telemetry | 指标批量、心跳 | 独立容量、拒绝无限缓存 |
| Control | 期望状态与上报结果 | 版本校验、审计、重放防护 |
| Artifact | 制品下载与校验 | 哈希、签名、限速、断点策略待定 |

指标兼容接口为 `POST /api/v1/metrics`，接收一个 MetricEnvelope。新增 `POST /api/v1/metrics/batch` 接收 `{batch_id, device_id, snapshots}`，要求同设备、样本时间严格递增；调用方显式配置最大快照数和请求体字节数。路由必须绑定 HTTPS 设备 Token，并注入支持原子追加和按 batch ID 幂等去重的历史写入器。Agent 有显式快照条数、队列字节数、批次条数及请求体大小限制的内存 `BatchQueue` 和 `SendBatch` API，`SendPending` 按调用方触发单次发送，只有收到有效接收响应才确认队列内容；pending 批次可在确认前以相同 ID 重试。`cmd/agent` 已提供默认关闭的 `metrics_batch` 装配，只有 HTTPS、设备 Token 和全部容量上限显式有效时才启用；队列仍不持久化、不创建独立采样或发送定时器，队列满会拒绝新样本。Server 批量 Handler 在并发乱序上报时仅将采集时间晚于现有当前态的样本发布为最新状态。`cmd/server` 现可在同时显式配置 HTTPS、设备凭据、私有 SSD 存储目录、单批快照上限和请求体上限时挂载该路由；SQLite 写入器将批次摘要与完整快照在 `metrics.db` 的同一事务中提交，并在重启后继续按 batch ID 去重。所有相关参数为空时保持原有单快照兼容接口，不创建数据库。

## 插件状态 API 初始边界
Server 内部提供以下 Agent 插件状态 Handler，作为 P1 控制面接口基础：

| 方法与路径 | 语义 |
|---|---|
| `GET /api/v1/agent/plugins/desired?device_id=...` | 返回该设备的期望插件状态快照 |
| `POST /api/v1/agent/plugins/reported` | 接收一个设备的批量已观察/已应用状态 |

期望状态响应为 `{ "device_id": "...", "states": [DesiredState, ...] }`；上报请求为 `{ "device_id": "...", "states": [ReportedState, ...] }`，成功返回 HTTP 202 和接收条目数。两条路由都要求 HTTPS 和绑定到请求 `device_id` 的 Bearer Token。批量上报在版本校验、设备范围校验全部通过后一次提交；过期状态拒绝整批。Handler 依赖窄化的状态存储接口，当前 Registry 实现仅驻留内存。`cmd/server` 尚未挂载这两条路由；Agent 客户端已实现，但未接入主循环。只有确定凭据安全加载方式及状态持久化边界后才启用路由。制品下载、激活和回滚不属于这些 API 的行为。

## 设备认证建议
TLS 校验 Server；各 Agent 独立设备级令牌（服务器只存校验凭据，scope 绑定 Device ID），预留未来 mTLS 和 CA 轮换。首次注册采用一次性批准及最小范围 bootstrap 凭据。令牌不与用户 Session 或 Newifi SSH 密钥共享。mTLS 是否首版替代 Token 保留实施前安全复核，不影响数据模型。

## 故障策略
Server 不可达时不修改插件运行状态，不无限重试；上报缓存有界，不做离线历史补录；Server 恢复后对账并重检当前数据有效时间。网络通道绝不提供任意 Shell/脚本/端口转发能力。
