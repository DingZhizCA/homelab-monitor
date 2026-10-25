# Widget、仪表盘发布与布局版本架构

> **文档状态**：架构规划与当前落地边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 结构模型
Dashboard 是用户管理的页面，Widget Instance 引用稳定的 Metric Descriptor 和查询参数，Layout 保存各断点下的位置/尺寸。内建指标继续使用 `descriptor_id`；插件指标使用结构化 `plugin_metric` 保存 plugin/version/collector/metric，并复用 Widget 的 `device_id`，两种引用互斥。Widget 配置不是代码和查询脚本，只有受控指标/维度选择和有限聚合表达。

## 类型与扩展
第一版数据视图包含指标卡、时间序列、状态、设备列表、流量排行、分类流量、Wi-Fi/Tailnet 客户端、告警列表、简单拓扑。新指标通过 Descriptor Registry 暴露，不必在前端硬编码每台设备的监控能力；未知、不支持指标不可自动显示为正常。

## Dashboard 权限（已确认）
| 类型 | Owner | Viewer |
|---|---|---|
| 个人 Dashboard | 创建编辑 | 创建编辑自己的 |
| 共享 Dashboard | 草稿、预览、发布、回滚 | 浏览、复制成个人版本 |
| 系统模板 | 复制成自有仪表盘 | 复制成自有仪表盘 |

## 发布模型
共享 Dashboard 分离 Draft Revision 和 Published Revision。Owner 在草稿中编辑、预览并发布；Viewer 只看到稳定已发布版本。回滚创建新发布版本，不删除旧版本历史。个人 Dashboard 支持防抖草稿保存与冲突检测。

## 多端布局
桌面网格 + 移动端单列，首建自动衍生手机布局，随后允许独立调整；从手机改布局不能无意破坏桌面设置。布局 JSON 导入/导出需版本校验、去掉秘密及无权限引用。

## 查询预算
同指标不同 Widget 共享 Server 缓存；全局时间筛选与单 Widget 覆盖并存；大时间范围切换聚合层，限制点数、并发和活跃查询。任何新增 Widget 都不能增加设备侧实际采样频率。

## 隐私权限
Owner 发布前验证 Viewer 可读范围。即使共享 UI 配置包含历史敏感引用，Query Service 也必须再次执行行/字段级授权，不因公开 Dashboard 配置而升级 Viewer 权限。

当前 `internal/dashboard` 已实现不含脚本/表达式的 Widget 与响应式布局数据结构、结构校验，以及个人编辑、Owner 共享草稿/发布/回滚和复制操作的服务边界。`MemoryRepository` 保留给开发与单元测试；`storage.DashboardRepository` 将 Dashboard、不可变 Revision 和发布指针保存到 `core.db`，在同一事务复核当前 Actor 状态、所有权和 CAS 前置条件。共享 Dashboard 的创建、草稿保存、发布和回滚同时提交无秘密载荷审计事件；回滚复制历史文档为更高的新发布 Revision，不覆盖旧历史。创建 ID 由仓储分配，因此仓储契约在 ID 产生后、提交前构造带实际目标 ID 的事件。重开、并发 CAS、草稿隔离、发布历史、回滚、禁用 Actor 和失败零写入均有本机测试。

`MetricDescriptor` Registry、最新快照和历史查询 API 已实现。路由只有在调用方提供字段级 `MetricAuthorizer` 时才能构造；授权先于数据读取。共享草稿发布使用与查询服务相同的内建 Registry 校验 Viewer 可读 Descriptor，未知引用会在发布指针变更前拒绝；结构化插件引用则按设备和完整版本身份精确查询已观测目录。Server 只有同时装配插件目录与插件历史查询面时才为共享发布启用该查找，否则拒绝插件引用；每次实际查询仍独立鉴权。`cmd/server` 在人类认证完整启用且显式提供 `--dashboard-max-body-bytes` 时装配 SQLite Dashboard 仓储和路由，默认保持关闭。Web 编辑页可按来源选择内建或插件指标，保存 Widget/布局、发布草稿、查看不可变发布历史并以新 Revision 回滚；插件选项来自当前设备的已观测目录，保存完整版本引用。内建 Widget 的手动最新值读取和 Explore 历史图都不轮询或启动设备采集。插件 Widget 提供无默认值的起止时间和点数上限，用户提交后调用版本化历史 API；Gauge、Counter、Event 的类型化数值可绘图，State 与非数值结果保留明细。最新内建值来自 Server 当前状态，批量持久化设备可在重启时恢复。内建持久化历史按显式窗口拼接日、小时、分钟和原始层，聚合点标记为 `estimated`，底层原始适配器保留兼容能力，但 API 尚未返回精确粒度元数据。插件 Explore 与 Widget 预览使用相同的类型化展示语义。敏感操作再认证、系统模板持久化和更细粒度 Viewer 设备/字段 ACL 仍未实现。
