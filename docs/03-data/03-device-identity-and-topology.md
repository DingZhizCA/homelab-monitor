# 设备身份、拓扑与时间化关联

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 身份原则
设备资产由内部稳定 Device ID 标识；IP、MAC、Agent ID、Tailscale Node ID 都是有来源和有效时间的 Identity Binding。不得单纯依赖同名主机或相同 IP 自动合并。

## 核心实体
| 实体 | 含义 |
|---|---|
| Device | 物理设备或明确的逻辑监控资产 |
| IdentityBinding | 设备与 IP/MAC/Tailnet/Agent 身份的时间化绑定 |
| Interface | 网络接口、AP、bridge port、隧道接口 |
| Observation | 原始采集证据、来源、时间、可信等级 |
| TopologyEdge | 两个节点之间的物理、路由或 Tailnet 关系 |
| IdentityDecision | 自动合并、Owner 确认、拆分、撤销及审计 |

## 匹配策略（已确认）
高可信 Agent 身份、已确认稳定接口身份可自动关联；LAN/Tailnet 跨身份匹配、随机 MAC、冲突名称等必须进入 Owner 待确认队列。支持合并、拆分、撤销，并保留历史归属操作记录。

## 时间化归属
一段网络流量按其发生时刻关联有效 IdentityBinding，而非使用今天的 IP 反推一年前的所有流量。若原始细粒度证据已清理，只剩汇总且无法可靠重分配，则维持 Unknown/Unresolved 标记。

## 三类拓扑
1. 物理/接入：Newifi LAN1→Alienware、LAN3→N1 已验证；Wi-Fi Station 仅表示关联历史快照。
2. 路由/出口：DHCP IPv4 网关可能指向 N1；同子网通信、IPv6 和 NAT 会改变实际数据路径，需证据区分。
3. Tailnet：Node、Peer、Exit Node 与 Direct/DERP/Peer Relay；Tailscale Direct 不等同于 sing-box Direct。

## 状态判断
设备在网状态与监控采集状态独立。DHCP 租约、STALE neighbor、FDB 存在和 Tailscale idle 不足以认定在线/离线。默认被动发现，Owner 可对单设备发起限时、有界主动诊断。

## UI 与性能
Devices 支持按名称、IP、MAC、Node、设备类型、采集质量筛选；拓扑连线显示来源与观测时间。拓扑查询复用已有采集快照，不默认全网扫描。
