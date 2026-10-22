# 告警引擎、规则版本与通知架构

> **文档状态**：规则、Incident 持久化与有界评估运行链已实现；通知架构待实施
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 首版明确范围
支持单指标与受限 AND/OR 组合，三值逻辑 True/False/Unknown；默认只启用已验证且低误报的基础核心设备模板。模板语义变更（阈值、恢复、级别、启用状态、通知）必须 Owner 审核，兼容性修复可正常升级。

## 领域对象
| 对象 | 含义 |
|---|---|
| RuleTemplate / TemplateRevision | 内置可复用定义与独立版本 |
| RuleInstance / RuleRevision | 某设备生效规则及不可变历史版本 |
| EvaluationResult | 每轮时间窗口、数据质量和三值结果 |
| AlertState | Pending/Firing/Recovering/Resolved 等状态 |
| Incident | 关联一组有明确证据的异常事实 |
| Silence / Ack | 通知静默、知悉确认，彼此不同 |
| Subscription | 用户端 Web Push/站内订阅 |
| DeliveryAttempt | 通知调度、失败及重试记录 |

## 模板继承
系统模板默认值 → 设备类别推荐值 → Owner 设备级覆盖，最终通过 Host/Server 的兼容、安全与评估资源上限。已生效规则绑定明确模板版本；系统升级不能静默改变规则语义。回滚通过新 Revision 保留审计。

## 评估与关联
复用 Server 已接收的当前快照和既有指标存储，不反向触发采集。当前调度器只从内存 Current State 提取内建 Descriptor，按快照在线状态把值标记为 Fresh 或 Stale，并将缺失值保留为 Missing；它不会请求 Agent、创建采集任务或改变设备采样周期。Incident 按明确依赖关系组合异常，关联不是无证据自动宣布根因。

## 通知（已确认）
Web 站内通知 + iPhone/桌面 Web Push；Critical/Warning 默认推送，Warning 受免打扰控制，Info 仅记录。通知调度器在 Alienware 上运行；支持分组、去重、静默、用户订阅、有限重试和恢复通知。Web Push 需 PWA/HTTPS/用户授权与外部推送服务；Server 整机停机或外网故障不承诺送达。

## 权限与安全
Owner 可以管理规则、静默、插件相关告警和告警版本；Viewer 可查获授权汇总与个人通知偏好，不能查看敏感连接明细或改规则。Push Payload 最小化，不包含凭据和敏感网址；点击通知需正常 Session 权限验证。

## 验收架构
使用模拟时钟和指标回放验证 AND/OR Unknown、规则重启、版本回滚、持久化 Incident、重复通知抑制、性能有界、通知失败隔离。禁止为了自动化验收主动停止 N1 的真实代理或路由服务。

当前 `internal/alerts` 实现纯规则结构校验与单轮三值评估：单指标比较、嵌套 AND/OR、Fresh 数据质量、设备/单位匹配；Stale、Missing、Unsupported 或跨设备数据都会成为 Unknown。规则可通过 `labels` 精确选择网络接口、挂载点等 Descriptor 维度；维度键必须与 Descriptor 声明完全匹配，缺少所选序列时结果为 Unknown，不会回退到其他维度。显式 `SamplesFromMetricResults` 适配器可把已授权 Dashboard 查询结果转换成评估输入，保留维度和数据质量；告警包不自行查询存储或触发采集。表达式深度和条件数上限必须由调用方显式提供，没有默认阈值或模板。

`AdvanceIncident` 提供无存储副作用的状态转移函数，覆盖 Inactive/Pending/Firing/Recovering/Resolved；待触发和恢复时长、Unknown 行为、Disabled 行为都必须由调用方显式提供。两个时长都使用必填指针，nil 会被拒绝；显式零时长表示对应状态立即越过，不隐含任何产品默认值。`hold` 保留原时间戳（等待期间墙钟仍继续计时），`reset_pending` 清除 Pending 或将 Recovering 还原为 Firing，`resolve` 关闭活动状态；Unknown 与 Disabled 的策略可分别设置。函数、SQLite 仓储和调度器都拒绝乱序评估及隐式 Rule Revision 迁移。规则删除或旧 Incident 迁移、通知去重/静默/订阅/失败重试仍未实现。

`MemoryIncidentRepository` 保留给开发和单元测试；`storage.IncidentRepository` 将相同的有界当前状态保存到 `core.db`。两者构造时都必须显式给出最大规则状态数，每次推进携带预期的上一评估时间并执行 CAS；相同时间、结果和 Revision 的重试幂等，不同并发推进只有一个成功。容量满时拒绝新规则状态，不淘汰既有状态；规则 Revision 变化明确返回迁移错误。SQLite 仓储经过进程关闭重开测试，并由显式启用的评估调度器装配；它仍不清理已解决状态或生成通知。

`Engine` 以同步单轮执行为核心，由 Server 生命周期启动和停止。每轮具有显式超时与全局规则数上限，规则仓储按 `(device_id, rule_id)` 稳定游标在 SQLite 查询阶段分页，避免先无界载入再截断；下一轮从上次位置继续并在末尾回绕，防止排序靠后的规则长期饥饿。设备枚举、规则读取、样本转换、表达式评估和 Incident CAS 的失败均形成结构化观察；单条规则失败不会中止其他规则。当前规则 Revision 与已有 Incident Revision 不一致时保留旧状态并报告错误，不静默重置活动 Incident。调度首次启动立即执行一轮，之后按显式周期串行运行，不会产生重叠轮次。

`MemoryRuleRepository` 保留给开发和单元测试；`storage.AlertRuleRepository` 将规则当前指针和不可变 Revision 保存到 `core.db`。两者都以 `(device_id, rule_id)` 隔离规则，要求调用方提供期望当前 Revision，历史按 Revision 倒序读取；回滚读取历史内容并以调用方明确给出的更高新 Revision 提交，不覆盖旧历史。SQLite 写入事务重新检查当前 Owner，CAS 冲突和审计校验失败均不部分写入，相同 Revision 与内容的重试幂等且不重复审计。`RuleService` 和 `AlertRuleHandler` 继续执行 Owner、HTTPS Session、Origin、严格 JSON 和请求体边界。`cmd/server` 只有在认证完整启用，并显式给出请求体、表达式深度和条件数上限时装配规则路由；没有默认阈值或模板。敏感操作再认证、规则删除与旧 Incident 迁移仍未实现。
