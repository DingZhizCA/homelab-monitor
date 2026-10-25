# Web 导航、页面架构与访问体验

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 技术基线
前端沿项目现有 React + TypeScript + Vite 方向，统一通过受权限控制的 Go Server API 查询。UI 框架/图表组件版本由实际开发阶段选定；不得让浏览器以每个 Widget 为单位请求 N1/Newifi 重新采集。

## 信息架构
| 一级页面 | 架构职责 |
|---|---|
| Dashboards | 个人与共享 Dashboard，编辑、草稿、发布和回滚 |
| Devices | 设备身份、状态、生命周期、Collector 和个性化详情 |
| Network | 物理接入、路由、Tailnet 三视角及设备流量账本 |
| Explore | 临时多设备指标对比、时间范围联动，保存为 Widget |
| Alerts | Incident、规则、事件、静默与通知状态 |
| Settings | 用户、设备凭据、插件、采样、存储与安全配置 |

## 页面与 API 分离
Web 使用统一 Query Service 的 HTTP JSON 接口，不直接连接 Agent、插件或原始时序库。实时数据优先采用 SSE 订阅 Server 已缓存/聚合的变化，回退短轮询；SSE 不改变采集节奏。写操作走带权限、CSRF、版本/幂等校验的 HTTP API。

## 响应式方案
桌面端多栏布局和可配置导航；移动端单列 Widget、底部高频入口及全屏图表。PWA 安装服务和通知权限属于独立模块，不通过普通 Web 页面静默申请 Push。

## 设备页模板
每类设备提供默认详情页；用户可以在自己的 Layout 中移动、显示、隐藏 Widget。新增 Collector 推荐新 Widget，不直接覆盖现有用户布局。Viewer 的布局编辑仅修改个人显示设置，不修改系统采样规则。

## 数据正确性
所有图表显示时间范围、聚合精度、数据最后时间、来源与质量；离线、stale、missing、unsupported、partial 不得展示为“全部正常”或零值。多端展示可重复查询但不能触发 Agent 任务。

当前实现已有概览、网络接口、临时对比、认证态仪表盘和告警导航视图。网络接口页只展示 Agent 最新快照中的接口计数和速率，并明确不代表 LAN/互联网/Tailnet 流量账本；同时展示采样周期、Agent 采样时间和 Server 接收时间。由于 `MetricEnvelope` v1 没有首样本/计数器重置质量标志，页面提示 0 速率也可能表示缺少差值基线。临时对比页只使用浏览器内最近 60 条样本，支持跨设备比较 CPU、内存和网络速率。仪表盘编辑页通过 Dashboard API 创建个人页面或 Owner 共享草稿，允许按设备选择内建 Descriptor 或已观测的精确版本插件 Descriptor，添加时间序列 Widget、移除 Widget、保存布局和发布共享草稿；Owner 还可查看共享发布版本并回滚到旧版（回滚会产生新版本）；Viewer 不展示该管理操作，也不会为只读浏览加载插件目录。页面只编辑受控结构，不执行查询脚本。内建 Widget 可由用户手动查询一次 Server 最新快照并查看值、单位和质量状态；插件 Widget 提供显式起止时间和点数上限，提交后按保存的完整版本身份查询并显示分辨率、质量、类型化明细及可绘制的数值序列。两种预览都不轮询，也不触发 Agent 采集。Explore 的内建指标表单在用户选择设备、Descriptor、起止时间及点数上限后才调用历史 API；图表按 Descriptor 维度分序列，缺失值保持为断点并展示返回质量计数、来源、时区、查询范围及最近样本时间。内建 History API 可按显式窗口返回日、小时、分钟聚合和原始数据，聚合点标记为 `estimated`，每点同时返回实际秒级分辨率及 `raw` 或 `gauge_mean` 聚合种类，UI 汇总展示结果中实际出现的粒度。Explore 的插件指标表单先按设备读取已观测 Descriptor Catalog，再以插件 ID、插件版本、Collector 和 Metric 的精确组合查询历史，禁止跨版本合并。插件结果明确显示原始或秒级分辨率、`aggregation_kind`、质量（含 `partial`）和类型化明细；Gauge、Counter 和 Event 分别用 mean、delta 和 event count 绘图，State 与非数值原始值保留为明细。两个表单均只执行显式读取，不轮询、不启动 Collector，也不会回退成本地伪历史；对应可选 Server 路由未连接时会明确报告不可用。告警页供 Owner 与 Viewer 按设备查看规则 Revision 的当前 Incident 转换状态，进入页面和切换设备时各读取一次，也可由用户手动刷新；它明确区分当前状态与告警历史，API 未启用或设备尚无状态时给出不同提示。Owner 可在同一设备范围内创建或修订单指标规则，所有业务字段均需显式填写，保存时递增 Revision 并携带当前版本 CAS；已有 AND/OR 规则以完整摘要只读展示。规则历史/回滚、复杂表达式编辑、Incident 历史、确认、静默和通知入口尚未实现。拓扑和目标环境历史存储验收仍待实现。

认证启用时，前端先读取 `/api/v1/auth/session`；未登录显示同源登录表单，成功后才加载设备 API、SSE 和 Presence。认证 API 返回 404 时进入旧版兼容路径。登录页提供首次 Owner 初始化表单；认证态导航栏提供注销，Owner 可进入账户页创建 Owner 或 Viewer，并查看、禁用及修改账户角色。`cmd/server` 在完整显式配置 SQLite、HTTPS Origin、Session、限速和历史预算时接入持久化认证与指标查询；默认运行仍返回认证 API 404。
