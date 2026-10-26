# 告警引擎、规则版本与通知架构

> **文档状态**：规则、Incident 持久化、有界评估、历史查询、显式订阅仓储、自助 API 与 Web 管理、通知意图/outbox、收件人 fanout 记录、单轮 Processor、退避重试和死信迁移已实现；消费调度已实现；死信管理 API 与实际 Push 待实施
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
Web 站内通知 + iPhone/桌面 Web Push；系统没有隐式收件人，用户必须通过精确订阅选择设备、规则、单一级别及站内和/或 Push 渠道。Critical/Warning 可选择 Push，Warning 后续受免打扰控制，Info 只能选择站内记录。当前纯规划器把已验证的不可变 Incident 转换和对应 Rule Revision 的 severity 转换为确定性意图：首次进入 Firing 生成 firing，已经 Firing 或 Recovering 后进入 Resolved 生成 recovery；Pending 等中间变化及 Recovering 回到 Firing 不产生通知。Critical/Warning 意图标记为 `push_eligible`，Info 标记为 `record_only`；ID 由转换 ID 与种类确定，重算不会产生新身份。severity 由推进该转换的规则快照显式传入，不改变已有转换事件协议。通知调度器后续在 Alienware 上运行；精确订阅、有界收件人规划及 SQLite 当前订阅仓储已实现；自助订阅服务和 HTTP 路由可由 `cmd/server` 在完整显式配置下装配；Alerts 页面由用户填写设备范围内的条数上限后手动读取，并明确新建或修订精确订阅。处理时订阅匹配与原子用户通知记录已实现；分组、静默、有限重试和实际 Push 尚未接入。Web Push 需 PWA/HTTPS/用户授权与外部推送服务；Server 整机停机或外网故障不承诺送达。

## 权限与安全
Owner 可以管理规则和告警版本；Owner 与 Viewer 可通过 HTTPS Session 按一个明确设备读取当前 Incident 转换状态及自己的精确订阅。订阅列表请求必须同时给出唯一 `device_id` 和正整数 `limit`，避免跨设备截断。当前查询没有更细粒度设备 ACL，也不包含历史、确认、静默或通知偏好；Viewer 不能改规则。Push Payload 最小化，不包含凭据和敏感网址；点击通知需正常 Session 权限验证。

## 验收架构
使用模拟时钟和指标回放验证 AND/OR Unknown、规则重启、版本回滚、持久化 Incident、重复通知抑制、性能有界、通知失败隔离。禁止为了自动化验收主动停止 N1 的真实代理或路由服务。

当前 `internal/alerts` 实现纯规则结构校验与单轮三值评估：单指标比较、嵌套 AND/OR、Fresh 数据质量、设备/单位匹配；Stale、Missing、Unsupported 或跨设备数据都会成为 Unknown。规则可通过 `labels` 精确选择网络接口、挂载点等 Descriptor 维度；维度键必须与 Descriptor 声明完全匹配，缺少所选序列时结果为 Unknown，不会回退到其他维度。显式 `SamplesFromMetricResults` 适配器可把已授权 Dashboard 查询结果转换成评估输入，保留维度和数据质量；告警包不自行查询存储或触发采集。表达式深度和条件数上限必须由调用方显式提供，没有默认阈值或模板。

`AdvanceIncident` 提供无存储副作用的状态转移函数，覆盖 Inactive/Pending/Firing/Recovering/Resolved；待触发和恢复时长、Unknown 行为、Disabled 行为都必须由调用方显式提供。两个时长都使用必填指针，nil 会被拒绝；显式零时长表示对应状态立即越过，不隐含任何产品默认值。`hold` 保留原时间戳（等待期间墙钟仍继续计时），`reset_pending` 清除 Pending 或将 Recovering 还原为 Firing，`resolve` 关闭活动状态；Unknown 与 Disabled 的策略可分别设置。函数、SQLite 仓储和调度器都拒绝乱序评估及隐式 Rule Revision 迁移。可选 SQLite 转换历史只在状态真正变化时记录前后状态、结果快照、Rule Revision、UTC 时间和确定性事件 ID，并与 current CAS 更新原子提交；状态不变的评估不重复写事件。规则删除仍未实现；通知已有不执行 I/O 的确定性意图规划、基础去重键和 SQLite 原子 outbox 适配器，outbox 已有界读取和幂等确认，单轮 Processor 可隔离每项失败；精确订阅、有界收件人规划和带 CAS/审计/容量的 SQLite 仓储已实现；自助订阅服务和 Handler 已可选装配；四项订阅请求/容量参数必须全部为正值，全部省略时关闭，部分配置拒绝启动。fanout Handler 会在处理时读取仍启用账户的精确订阅，并把一个意图的全部用户记录原子写入；无收件人时允许消费完成，容量失败保留 outbox。静默、站内读取、死信管理 API 和实际 Push 仍未实现。

`MemoryIncidentRepository` 保留给开发和单元测试；`storage.IncidentRepository` 将相同的有界当前状态保存到 `core.db`。current 和 superseded 归档都必须显式给出最大条数；每次推进携带预期的上一评估时间并执行 CAS，相同时间、结果和 Revision 的重试幂等，不同并发推进只有一个成功。current 容量满时拒绝新规则状态，不淘汰既有状态；Rule Revision 变化明确返回迁移错误并暂停该规则推进。Owner 可向 `POST /api/v1/owner/alerts/incidents/activate` 提交设备、规则和目标 Revision。仓储在一个事务内复核启用 Owner、规则当前 Revision、旧 Incident Revision 和最后评估时间，把完整旧状态写入以旧 Revision 唯一标识的不可变 `superseded` 归档，删除 current 并提交审计；无旧 current 或已经属于目标 Revision 的重试幂等返回 ready。归档容量满、目标已变化或并发评估获胜时不部分写入。下一轮评估为目标 Revision 创建干净状态，不继承旧 Pending/Firing/Recovering 时间。

SQLite 仓储由显式启用的评估调度器装配。相同运行时把该仓储注入 `IncidentService`、激活服务、归档查询服务和 HTTP Handler；`GET /api/v1/alerts/incidents` 要求有效 Owner 或 Viewer HTTPS Session，并且查询参数只能包含一个非空、已裁剪的 `device_id`。返回值按 Rule ID 稳定排序，只表示每条规则的当前转换状态；查询不会推进评估、触发采集或推断历史。Web 告警页按同一设备范围展示状态、最近评估结果和转换时间，只在进入、切换设备或手动刷新时查询；当规则与 current Revision 不一致时，仅向 Owner 显示包含两个版本号的归档并激活按钮。`GET /api/v1/alerts/incidents/history` 允许 Owner 与 Viewer 按唯一设备、规则和显式正整数 `limit` 从新到旧读取 superseded Revision；上限不能超过启动时配置的归档总容量。Owner Web 在规则历史区提供手动查询且不预填上限，也不自动读取。可选历史保留任务要求周期、保留时长、每集合单轮删除量和超时完整显式配置，分别按时间最旧优先删除严格早于截止时间的 superseded 归档与状态转换；四项均未配置时关闭，部分配置拒绝启动。转换事件持久化还要求显式总容量并强制依赖该保留任务，未启用时维持 current-only 兼容路径。任务不删除 current、规则历史或审计事件。`GET /api/v1/alerts/incidents/transitions` 仅在转换历史启用时挂载，允许 Owner 与 Viewer 按唯一设备、规则和显式正整数 `limit` 从新到旧读取事件；空结果为数组，查询不推进评估或触发采集。Owner Web 在规则历史区以独立、初始为空的条数上限手动读取并显示前后状态、Revision、评估结果和时间；不会自动查询。仓储仍不清理已解决 current。可选 outbox 适配器要求独立显式容量，并把有通知意义的转换、完整意图和 current CAS 放入同一事务；容量满时三者一起失败，状态不变和中间转换不占用 outbox。独立 outbox 仓储按明确批量上限读取已到期项并以创建顺序处理，复验完整记录，并在下游成功后按意图 ID 幂等确认和删除；确认前崩溃允许重复交付，所以下游必须以意图 ID 去重。无 goroutine 的单轮 Processor 对每项先调用幂等 Handler 再确认；Handler 失败按显式最大次数、初始/最大退避和错误字节上限记录下一次时间，达到次数上限时原子移入有显式容量的死信表。仓储支持显式上限查询、复核 outbox 容量并重置尝试状态的原子重入，以及按截止时间和单批上限清理最旧死信。确认失败不增加投递尝试次数。处理、迁移或确认失败均带阶段观测并继续后续项，读取失败结束本轮。Server 仅在所有通知调度、批量、容量、重试和错误长度参数完整显式配置时接入该链；首次立即执行，之后按周期串行执行，并随 Server context 停止。死信管理 API 和实际 Push 尚未接入。

`Engine` 以同步单轮执行为核心，由 Server 生命周期启动和停止。每轮具有显式超时与全局规则数上限，规则仓储按 `(device_id, rule_id)` 稳定游标在 SQLite 查询阶段分页，避免先无界载入再截断；下一轮从上次位置继续并在末尾回绕，防止排序靠后的规则长期饥饿。设备枚举、规则读取、样本转换、表达式评估和 Incident CAS 的失败均形成结构化观察；单条规则失败不会中止其他规则。当前规则 Revision 与已有 Incident Revision 不一致时保留旧状态并报告错误，不静默重置活动 Incident；只有上述 Owner 激活事务成功后才恢复推进。调度首次启动立即执行一轮，之后按显式周期串行运行，不会产生重叠轮次。

`MemoryRuleRepository` 保留给开发和单元测试；`storage.AlertRuleRepository` 将规则当前指针和不可变 Revision 保存到 `core.db`。两者都以 `(device_id, rule_id)` 隔离规则，要求调用方提供期望当前 Revision，历史按 Revision 倒序读取；回滚读取历史内容并以调用方明确给出的更高新 Revision 提交，不覆盖旧历史。SQLite 写入事务重新检查当前 Owner，CAS 冲突和审计校验失败均不部分写入，相同 Revision 与内容的重试幂等且不重复审计。`RuleService` 和 `AlertRuleHandler` 继续执行 Owner、HTTPS Session、Origin、严格 JSON 和请求体边界。`cmd/server` 只有在认证完整启用，并显式给出请求体、表达式深度和条件数上限时装配规则路由；评估和激活还要求显式 current 与归档容量，没有默认阈值或模板。Web Owner 编辑器从 Descriptor Registry 读取单位与维度定义，只允许创建或无损回填单指标表达式；Owner 必须明确选择级别、启用状态、运算符并填写有限阈值和维度值。已有 AND/OR 规则只读展示，保存以当前 Revision 作为 CAS 并写入递增的新 Revision。Owner 可按需读取任意规则的不可变历史，并明确选择旧 Revision 重新发布；浏览器只发送目标、期望当前版本和下一个版本号，Server 读取和复制完整历史内容。复杂表达式编辑、敏感操作再认证和规则删除仍未实现。
