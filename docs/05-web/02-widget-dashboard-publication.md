# Widget、仪表盘发布与布局版本架构

> **文档状态**：架构规划与当前落地边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 结构模型
Dashboard 是用户管理的页面，Widget Instance 引用稳定的 Metric Descriptor 和查询参数，Layout 保存各断点下的位置/尺寸。Widget 配置不是代码和查询脚本，只有受控指标/维度选择和有限聚合表达。

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

当前 `internal/dashboard` 已实现不含脚本/表达式的 Widget 与响应式布局数据结构、结构校验，以及个人编辑、Owner 共享草稿/发布/回滚和复制操作的服务边界。新增的 `MemoryRepository` 在进程内原子执行版本比较、所有权过滤、共享发布历史和回滚新版本，适用于开发与测试；共享 Dashboard 的创建、草稿保存、发布和回滚会在同一锁内追加无秘密载荷的审计事件，失败的版本比较或事件校验不会部分提交。创建 ID 仍由仓储分配，因此仓储契约接收事件工厂，在 ID 产生后、提交前构造带实际目标 ID 的事件。数据和事件仅存内存，进程重启即丢失，也不能检查当前用户是否仍启用，因此不能作为生产仓储。Server 提供可选 Dashboard HTTP Handler，采用 HTTPS Session、变更 Origin 校验、严格 JSON 解码和显式请求体上限；只有通过 `NewRouter` 注入对应认证 Handler 才会挂载。Owner 可通过 `GET /api/v1/dashboards/{id}/revisions` 查看已发布版本，Viewer 不可查看该历史；回滚仍通过带当前 Published Revision 前置条件的 POST 执行，并生成新发布 Revision。Session、Router、Service 与内存仓储的集成测试覆盖草稿可见性、发布、历史读取、回滚新版本、审计顺序、未发布编辑隔离及 Viewer 写入拒绝。Web Owner 页面可查看发布版本、预览目标版本的 Widget 与布局，并触发回滚；预览与回滚使用同一发布历史响应中的不可变文档。生产环境仍需仓储在数据库事务中执行版本比较、所有权、当前 Owner 状态和审计提交，并增加敏感操作再认证；`cmd/server` 未装配 Dashboard 路由。

`MetricDescriptor` Registry 和最新快照查询 API 也已实现。路由只有在调用方提供字段级 `MetricAuthorizer` 时才能构造；授权先于快照读取。共享草稿发布还必须注入 `PublicationPolicy`，该策略在仓储读取指定不可变草稿版本后校验 Viewer 可读范围，校验通过后仓储仍以版本比较提交发布；未实现该策略时 Dashboard Service 无法构造。Web 编辑页已连接 Dashboard API，可保存 Widget/布局并发布草稿；Owner 还可查看共享 Dashboard 的发布历史并以新 Revision 回滚。每个 Widget 支持用户主动读取一次最新值和质量状态；它不轮询，不启动设备采集。Explore 有依赖可选 History API 的显式历史趋势图。生产路由未由 `cmd/server` 装配；当前最新值仅来自 Server 内存中的最后一份快照，历史查询没有生产持久化/聚合适配器。
