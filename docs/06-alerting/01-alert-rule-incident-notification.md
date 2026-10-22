# 告警引擎、规则版本与通知架构

> **文档状态**：架构规划与单轮规则评估已部分实现
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
复用既有指标数据库和缓存，不反向触发采集。对采样缺失、Collector 降级和 Agent 心跳失败分别处理，避免把“无数据”判为零或已恢复。Incident 按明确依赖关系组合异常，关联不是无证据自动宣布根因。

## 通知（已确认）
Web 站内通知 + iPhone/桌面 Web Push；Critical/Warning 默认推送，Warning 受免打扰控制，Info 仅记录。通知调度器在 Alienware 上运行；支持分组、去重、静默、用户订阅、有限重试和恢复通知。Web Push 需 PWA/HTTPS/用户授权与外部推送服务；Server 整机停机或外网故障不承诺送达。

## 权限与安全
Owner 可以管理规则、静默、插件相关告警和告警版本；Viewer 可查获授权汇总与个人通知偏好，不能查看敏感连接明细或改规则。Push Payload 最小化，不包含凭据和敏感网址；点击通知需正常 Session 权限验证。

## 验收架构
使用模拟时钟和指标回放验证 AND/OR Unknown、规则重启、版本回滚、持久化 Incident、重复通知抑制、性能有界、通知失败隔离。禁止为了自动化验收主动停止 N1 的真实代理或路由服务。

当前 `internal/alerts` 实现纯规则结构校验与单轮三值评估：单指标比较、嵌套 AND/OR、Fresh 数据质量、设备/单位匹配；Stale、Missing、Unsupported 或跨设备数据都会成为 Unknown。规则可通过 `labels` 精确选择网络接口、挂载点等 Descriptor 维度；维度键必须与 Descriptor 声明完全匹配，缺少所选序列时结果为 Unknown，不会回退到其他维度。显式 `SamplesFromMetricResults` 适配器可把已授权 Dashboard 查询结果转换成评估输入，保留维度和数据质量；告警包不自行查询存储或触发采集。表达式深度和条件数上限必须由调用方显式提供，没有默认阈值或模板。

`AdvanceIncident` 现提供纯内存状态转移函数，覆盖 Inactive/Pending/Firing/Recovering/Resolved；待触发和恢复时长、Unknown 行为、Disabled 行为都必须逐次由调用方显式提供。两个时长都使用必填指针，nil 会被拒绝；显式零时长表示对应状态立即越过，不隐含任何产品默认值。`hold` 保留原时间戳（等待期间墙钟仍继续计时），`reset_pending` 清除 Pending 或将 Recovering 还原为 Firing，`resolve` 关闭活动状态；Unknown 与 Disabled 的策略可分别设置。函数拒绝乱序评估和隐式 Rule Revision 迁移；它不持久化、不生成通知事件、不处理规则删除或旧 Incident 的迁移，因此选定持久化及业务策略前不得接入 Server。Server 持久化规则、Owner 审核入口、Incident 存储、通知去重/静默/订阅/失败重试仍未实现或定案。

`MemoryRuleRepository` 现在提供仅供开发和测试的进程内规则修订仓储：以 `(device_id, rule_id)` 隔离规则，要求调用方提供期望当前 Revision；每个已存 Revision 不可变，历史 Revision 可读，回滚应以更高新 Revision 保存旧表达式。`RuleService` 只允许 Owner 读取或写入，并在写入前校验表达式和 Revision；Viewer 会被拒绝。Owner 保存规则时会生成无秘密载荷的管理事件，内存仓储在同一锁内提交新 Revision 和事件；CAS 冲突不写入，完全相同的重试不重复记录。该服务未接入 HTTP/Server，规则和审计事件都没有生产持久化，也没有敏感操作再认证；仓储本身不是授权边界，默认阈值仍未实现或决定。
