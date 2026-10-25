# Go 模块边界与依赖约束

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 模块划分
| 模块 | 所有权及数据 | 对外能力 |
|---|---|---|
| identity | Device、IdentityBinding、接口、信任来源 | 设备合并/拆分、解析、历史关联 |
| topology | 二层接入、路由、Tailnet 关系与证据 | 时间化关系查询 |
| metrics | Descriptor、Envelope、Counter/Gauge 校验 | 接收标准化指标 |
| ingestion | 批量入站、校验、幂等和限流 | 写入受控观测数据 |
| timeseries | 原始数据、分层汇总与质量位 | 查询、汇总、保留清理 |
| flow | Internet/LAN/Tailnet 计量与去重账本 | 分类流量查询 |
| plugins | 包、签名、版本、期望/实际状态 | 制品分发与部署管理 |
| collectors | 独立 Collection Policy、Remote Host / Agent 协同配置 | 按绑定插件 Revision 调度、限时执行 |
| alerting | 规则版本、评估、Incident、认证用户的当前状态查询 | 告警查询及状态迁移 |
| notifications | 投递任务、订阅、静默 | Push、站内通知 |
| auth | 用户、角色、Session、设备凭据 | 登录、鉴权、凭据生命周期 |
| dashboards | Dashboard、Widget、草稿与发布版本 | 受权布局操作 |
| query | 统一读模型、查询预算、缓存 | Widget、Explore、导出 API |
| operations | 自身指标、资源预算、审计 | 健康及管理 |

## Go 包职责原则
建议 `cmd/server` 与 `cmd/agent` 为启动入口；`internal/server/<domain>` 和 `internal/agent/<domain>` 隔离执行模式；`pkg/protocol` 或内部 versioned contract 保存跨进程公共模型。具体目录命名可以与现有仓库适配，不应直接覆盖当前目录结构。

- 每个模块暴露应用级接口，不允许跨域直接读写彼此的存储表。
- 插件跨进程契约是版本化 Protobuf；Go 内部模块接口无需在第一版全部定义为 RPC。
- 数据访问依赖方向为 Web/API → 业务域 → 仓储接口 → 存储实现；禁止基础采集依赖 Web 组件。
- 所有跨域命令携带调用者身份、目标 Device ID、截止时间、版本和审计上下文。
- 观察数据的事实字段保持不可变；归属关系和聚合派生值允许版本化更正。

## 当前仓储接入点
现有 `internal/server.Handler` 通过 `CurrentStateRepository` 访问当前设备快照与显式刷新任务；插件状态 API 通过独立的 `PluginStateStore` 访问期望/上报状态。当前实现仍是进程内内存存储，接口仅为后续持久化适配器提供替换点，不表示状态已经持久化。

本地人类用户的密码和服务端 Session 逻辑位于 `internal/auth`，HTTP 认证 Handler 位于 `internal/server`，其仓储接口与 Agent Device Token 校验相互独立。SQLite 适配器、分钟/小时/日聚合、分层历史源、PASSIVE/TRUNCATE checkpoint、保留、容量保护和 incremental vacuum 位于 `internal/storage`；这些管理器暴露显式单轮方法和运行循环，但不拥有隐式默认参数或自行创建 goroutine。`cmd/server` 只有在显式提供完整 HTTPS Origin、Session、限速、历史预算和存储维护参数时才注入 AuthHandler；启用聚合时还要求明确周期、工作量、超时和四层保留窗口。查询仍通过 `internal/dashboard` 的授权和预算边界，不直接依赖聚合任务实现。

`cmd/server` 只负责读取启动参数、构造当前实现并启动 HTTP Server；稳定 API 路径由 `internal/server.NewRouter` 装配。插件控制面在完整显式配置 SQLite、人类与设备认证、私有制品目录、单包/总量容量、每设备 Collection Policy 数量上限及有界清理参数时，使用 `storage.PluginRepository` 与 `FileArtifactRepository` 构造插件 Desired/Reported、独立 Collection Policy、制品下载、Owner 管理和制品维护；默认不挂载。业务 Handler 只依赖带 Context 与错误返回的仓储边界，数据库错误不会被静默转换为空 Desired State。Collection Policy 写入与审计同事务提交，并在事务内复核其绑定的插件 Revision 仍启用；新增策略键还在同一事务检查每设备容量。Owner 发布与制品维护共享应用层互斥边界，制品仓库自身再以进程内读写锁和跨进程文件锁保护读取、导入和删除。

## 任务调度与事件
定期采集由 Host/Agent 调度器负责；历史聚合、告警评估与通知由 Server 内部受控工作队列执行。第一版可使用有界内存队列加持久化任务状态，不引入外部 Message Broker。跨域事件需定义幂等键和失败重放策略。

## 模块测试边界
同域单测使用依赖注入；协议兼容测试验证 Protobuf、HTTP Schema；端到端测试用模拟插件和数据库测试实例，禁止以实际停用 N1 网络服务作为自动化测试手段。
