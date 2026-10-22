# Newifi D2 远程只读采集安全边界

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 设备职责与架构边界
Newifi D2/OpenWrt 192.168.10.1 负责 NAT、DHCP、Wi-Fi、主路由。首版默认不运行常驻 Go Agent，不写入历史数据库，不增加监控转发依赖。Alienware 远程 Plugin Host 执行少量合并、限频的只读查询。

## 现有可验证指标
- Bridge 端口编号 lan1=0x1、lan3=0x3，对应 Alienware / N1；lan2/lan4 当前无物理载波。
- Wi-Fi phy0-ap0 2.4GHz、phy1-ap0 5GHz 可读取 Station 的 RX/TX、signal、bitrate、retries、failed、connected_time 等。
- `/sys/class/net/<if>/statistics`、`carrier`、Bridge FDB、ARP、DHCP 租约及 `iwinfo` 为候选数据源；读取时仍有负载，频率待实测。

## 授权策略（已确认）
使用专用的监控访问身份和凭据，与 root 管理权限分离；优先评估受限 SSH 强制命令及固定采集白名单，其次评估带 ACL 的 ubus/rpcd 只读 API。必须实机核实 Dropbear 是否支持相关限制；不能只将 root authorized_keys 写成“只读”就假定具有 OS 级最小权限。

## 通信与执行限制
单机有界并发、少量合并查询、截止时间、受限输出长度和失败退避；禁止交互 Shell、SSH 转发、任意命令参数注入。禁止为了监控自动修改网络、防火墙、Wi-Fi、flow offload 或安装长期运行的管理 API。

## 数据来源与归属
端口 Counter 反映物理链路，不直接归属到互联网设备账本；Wi-Fi Station 是 AP 侧的累计量，会因重连重置，且包含本地传输。设备归属由 Server 的 Identity/Flow 统一完成。

## 实施前验证清单
确认受限权限模型、现有 SSH 服务性能与建连成本、批量命令持续耗时、查询频率上限、查询期间 NAT/Wi-Fi 的 p95 延迟及丢包变化，必须具备停止采集与撤销凭据的回滚流程。
