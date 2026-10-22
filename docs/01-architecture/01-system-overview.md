# 系统总体架构与数据流

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 架构样式
Homelab Monitor 采用在 Alienware 上部署的 Go 模块化单体 Server；N1 和 Alienware 各部署 Go Agent；OpenWrt 由 Alienware 的远程 Collector 插件主动只读采集。Collector 插件第一版采用原生独立进程，经本机 Unix Socket/gRPC 与 Host 交互。

## 分层结构
| 层级 | 所在节点 | 职责 |
|---|---|---|
| Presentation | 浏览器 / iPhone PWA | Dashboard、Devices、Network、Explore、Alerts、Settings |
| API / Auth | Alienware Server | HTTP API、认证授权、Session、审计、查询预算 |
| Domain | Alienware Server | Device Registry、Flow Attribution、Alert Engine、Dashboard、Plugin Registry |
| Collection Control | Server + Agent | 配置版本对账、插件分发、调度、资源守卫 |
| Collection Execution | Agent / Plugin Host | 收集事实指标，返回测量时间和质量信息 |
| Storage | Alienware SSD | 配置、指标、汇总、历史、事件、规则、通知状态 |

## 主要数据流
1. Server 管理用户经审批的期望状态和插件制品。N1 Agent 在认证连接上主动轮询，验证并实施本地安全边界。
2. N1/Alienware Agent 定期通过 HTTPS 批量上报指标；Newifi 的只读插件在 Alienware 远程读取现有接口。
3. Ingestion 校验 schema、设备身份和计数器语义，写入统一观测数据模型。
4. Identity 将 IP/MAC/Agent/Tailscale Node 等带时间窗口的网络身份映射到稳定 Device ID。
5. Flow Accounting 根据观测点、源设备、网络路径与计数域分类，进行有效增量、去重和覆盖率标注。
6. Storage 定期聚合，Alert Engine 读取已经采集的数据并评估规则，通知调度器根据用户订阅推送。
7. Web 的所有 Widget / Explore 查询同一受控 Query Service；页面请求不直接触发 Collector 采样。

## 禁止的依赖方向
插件不能直接操作全局数据库、用户管理和 Web Push；Server 不应向 Agent 下发自由格式 Shell 指令；Web / Widget 不应直接调用 Newifi/N1 采集 API；流量统计不得控制真实转发路径。

## 演进策略
保留模块化单体基础，不预先引入 Kafka、Kubernetes、服务发现、插件市场。性能瓶颈通过指标实测确认后再评估拆分。集成与模块 API 要先保持可替换的清晰界限，而不是过早微服务化。
