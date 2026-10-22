# 部署拓扑与系统依赖

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 当前已核实的物理连接
| 设备 | 接口与地址 | 业务职责 |
|---|---|---|
| 上级光猫/网关 | 192.168.1.1 | 外部接入 |
| Newifi D2/OpenWrt | WAN 192.168.1.2；LAN 192.168.10.1 | NAT、DHCP、Wi-Fi、主路由 |
| Alienware | Newifi LAN1；192.168.10.3 | Server、Agent、Web、数据库、Docker/Tailscale |
| N1 Armbian | Newifi LAN3；192.168.10.2 | sing-box、旁路由、Tailscale Exit Node、Agent |
| WLAN | phy0-ap0 2.4G、phy1-ap0 5G | 无线客户端接入 |

以上接线通过 OpenWrt ARP MAC、Bridge FDB 和 brport/port_no 验证；其他 Wi-Fi 客户端数量为历史快照，不能当作当前在线清单。

## 进程位置
- Alienware：Go Server、Web 静态资源/受控 API、本机 Agent、远程 Newifi Collector Plugin Host、业务元数据及指标数据库、通知调度器。
- N1：非 root Go Agent、独立进程插件与受限运行时；不部署历史库，不接管 sing-box 或 Tailscale 网络控制。
- Newifi：无新增常驻 Agent，保留现有 Dropbear/ubus 等可用接口，通过经认证的受限只读查询采集。

## 网络入口
Web 仅 LAN 与 Tailnet 访问；Agent 从 N1 主动连接 Alienware LAN 地址，不依赖 Exit Node；插件 IPC 使用 Unix Domain Socket，不开放新的 LAN 入站监听。远端浏览器通过 Tailscale 安全访问，LAN 端浏览器需独立可信 HTTPS 方案。

## 现有服务冲突
Alienware 的 Tailscale Serve 已曾为 CouchDB 提供 443 HTTPS 入口，新的 Web 必须先检查路径、域名和反向代理分配，不能覆盖既有服务。Server 与 Agent 使用独立受控服务端口或路由，具体端口实施阶段确定。

## 单节点容错范围
Alienware 按长期运行设计；整机关机允许无历史补录、无通知、无高可用切换。但 Newifi 和 N1 的网络职责必须独立于监控系统；Server 不可用时 Agent 限频重试，必要时减少或暂停非必需采集。

## 存储位置
监控数据库写入 Alienware SSD；禁止默认触发 1TB HDD 唤醒。Newifi 不持久化历史指标。Agent 仅保存运行必需的插件安装清单、当前版本、回滚元数据及少量状态，不保存大规模离线数据队列。
