# 全网流量归属、方向与去重架构

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 统计范围（已确认）
所有 LAN 有线/无线终端、Tailscale 远程客户端及 N1 Exit Node；每设备区分上传/下载，并尽量解释 Proxy、sing-box Direct、Bypass 和 Unknown。连接详情默认不长期记录，仅 Owner 启用短时诊断。

## 多观测点与计数域
| 来源 | 可靠可见内容 | 不可直接推出 |
|---|---|---|
| Newifi lan1/lan3 | 两条物理链路累计收发字节 | 具体终端互联网用量 |
| Newifi Wi-Fi Station | AP 与客户端无线收发、信号、重传 | 仅外网流量、Proxy/Direct |
| Newifi wan | 路由器 WAN 总流量 | 源终端身份或代理分类 |
| N1 eth0 | N1 网口字节 | 终端去重后的外网消费 |
| N1 singtun0 | TUN 可见流量 | 内核 Bypass 的完整流量 |
| N1 tailscale0 | 隧道接口字节 | 单 Peer Exit Node 外网用量 |
| sing-box route/outbound | Direct/VLESS 路由动作或有效计数 | 无 API 时精确每设备字节 |
| Conntrack | N1 已启用 Accounting；存在计数条件 | NAT、offload、旁路等路径的全覆盖 |

当前 Agent `MetricEnvelope` v1 上报接口累计计数和相邻采样计算的速率，但没有首个观测或计数器重置的质量字段。Agent 无法计算差值时保留默认零速率；因此消费者不能将 0 解释为已确认无流量。Network UI 明示该歧义并展示采样周期和 Server 接收时间。若要区分 zero、missing-baseline 和 reset，需设计并评审独立的向后兼容 Schema 版本/扩展，不能静默改变 v1 的 JSON 契约。

## 方向口径
无线 AP 视角 RX 为客户端发送（客户端上传）；AP TX 为发向客户端（客户端下载）。Server 需在统一 Metric Scope 中保存观察方向及终端相对方向，防止混用。设备互联网用量与 LAN 内部传输要分开，Tailnet Peer 传输和 Exit Node 转发不能混算。

## 初步归属算法
1. 在有可靠来源身份与网络路径的计数域中记录字节差值。
2. 关联事件发生时有效的 Device ID，保留 IP/MAC/Node 与 NAT 前后的证据。
3. 根据观测层定义“物理链路字节”“隧道传输字节”“逻辑出口字节”“设备用量账本”不同指标系列。
4. 跨层重复测量不得简单相加；同流量若被多个位置计数，只有经明确去重规则后才能进入设备账本。
5. 分流类别证据不充分时标记 Unknown；无采集覆盖时标记 Partial/Missing，不填零。

## IPv6 与旁路限制
此前已观察到部分终端 IPv6 可通过 Newifi RA 直连，不一定经过 N1；局域网同二层通信可能不经过 N1。N1 的 sing-box 现有 3 条 bypass、5 条 direct、2 条 vless，默认 vless；绕过流量不能单靠 TUN 计数获知。对未覆盖路径不得承诺全网 100% 代理准确率。

## Exit Node 特殊身份
远程客户端通过 Tailnet 到 N1 的数据必须在 NAT/转发前尽可能保留原始 Peer/源地址；Tailscale Direct/DERP 是隧道通信路径，不能直接解释为代理直连。需要独立验证 `tailscale0`、conntrack 与出口间的计量关联。

## 保护措施
默认不安装新的全流量抓包、Netfilter 规则或 eBPF 程序；如确需新的计量点，必须独立评估 CPU、流量转发时延、offload 兼容及撤销方案，并获得 Owner 单独批准。
