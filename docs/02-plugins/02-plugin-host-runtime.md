# Plugin Host、运行时与故障隔离

> **文档状态**：架构规划与当前落地边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 本地职责划分
Server Plugin Manager 管理期望版本；Agent Reconciler 管理本地期望/实际对账、健康检查和配置化失败退避/隔离；Supervisor 管理单个进程的启动与关闭；Scheduler 管理调用方提供的采集批次、并发与 Deadline；Runtime 负责本机 Unix Socket/gRPC。专用 launcher 已提供基础硬 rlimit 与 `no_new_privs`；cgroup 进程树总量、RSS、不同 UID、文件系统、系统调用和网络隔离仍未实现。

`PluginScheduler.CollectBatch` 在同一 Scheduler 实例的并发批次间共享并发上限，并对每个 outcome 一并捕获插件 ID、包版本和对应 Collector Descriptor；每个任务还使用 Collection Policy 的独立超时。Server 持久化独立 Collection Policy，并向设备认证的 Agent 提供严格有界读取；策略显式给出周期、超时、JSON 配置并绑定插件 Revision。Agent 以独立 tick 选择到期任务，在首次使用或 Policy/Plugin Revision 变化时先调用无副作用 `ValidateConfig`，成功后才允许 `Collect`；校验结果只对该双 Revision 缓存。成功 outcome 经 `NormalizePluginOutcome` 进入有界原子队列并通过独立幂等端点发送，单个任务错误仍隔离在对应 outcome。

## 进程与权限
- 每个插件包作为独立进程启动，不共享 Agent 地址空间。进程崩溃最多使其 Collector 不可用。
- Plugin Host 不应默认拥有 root Shell 执行能力。普通插件使用低权限系统身份或受控隔离；不同特权类别必要时分进程。
- 当前独立 launcher 已施加每进程继承的 FD、进程数、单文件大小、虚拟地址空间、累计 CPU 时间硬限制及 `no_new_privs`。计划中的 systemd/cgroup v2 MemoryMax/High、CPUQuota/Weight、TasksMax 仍需在 Armbian 和 Ubuntu 现网只读验证非 root 管理和委派能力。
- Unix Socket 放在受保护的运行目录，限制文件系统权限和 Peer 身份；禁止绑定对外 TCP 地址。
- 插件若需要访问 OpenWrt、sing-box、Tailscale，应通过明确授权的受控代理/Adapter 获得有限能力，不能凭 Manifest 声明就获取任意凭据。

## 生命周期状态
Installed → Verified → Staged → Starting → Running → Degraded/Failed → Stopping → Stopped；插件可进入 Quarantined，等待 Owner 审查。每个状态变化记录原因、Plugin Version、耗时及资源用量。

## 调度与熔断
Collector 共用受控时间轮/任务池；插件进程中多个 Collector 的调用也要限制总并发。若超时、崩溃或超预算，执行指数退避和启动次数限额；超过阈值隔离插件、停止重试并保留当前业务服务。禁止通过频繁重启 N1 Agent 掩盖插件问题。

## 性能保护测试
模拟插件阻塞、Panic、OOM、CPU busy loop、过大输出、异常日志；验收项为 Plugin Host 可恢复、Agent 不被耗尽、N1 sing-box/Tailscale 转发路径与配置均未被影响。受限进程是可靠性边界，不等于能够安全执行任何不可信原生二进制。

## 当前代码落地边界
插件基础代码已可复验暂存制品、启动经注入的进程句柄、完成 gRPC 握手、核对 Manifest 与 Describe 的 Collector 列表，并检查健康状态。Plugin Host 的每种 RPC 操作都要求调用方提供 deadline；调用方驱动的批次调度器限制并发，不创建定时器或改变 Agent 采样周期。Agent 对账会在显式控制轮询中检查已管理进程健康；失败时优先恢复最近一个已应用版本（若存在），否则重启当前暂存版本；连续失败按调用方必须提供的上限和指数退避隔离同一 Desired Revision。Linux `ExecSupervisor` 在生产装配中通过专用 launcher 无竞态地设置硬 rlimit 和 `no_new_privs` 后启动独立子进程，并按截止时间关闭；它仍没有 UID 降权、systemd/cgroup 总量控制或完整内核沙箱，因此不能作为生产安全隔离，也未完成目标设备验收。

Host 和插件服务端的 `ValidateConfig` 都要求 Collector 已通过 `Describe` 声明；未描述的 ID 在调用插件实现之前即被拒绝。此检查与采集调用的描述校验一致。

插件侧 `ServeUnixPlugin` 只在已有私有运行目录中创建 Unix Socket，权限限制为所有者读写，拒绝覆盖已有路径，并在进程上下文结束后清理自己创建的 socket。若多个插件仍使用同一 OS 用户，0600 socket 权限不能防止同 UID 插件互访；运行用户隔离仍由未实现的 Supervisor 负责。

Agent staging 会把已验签的制品保存为 owner-only 可执行文件（0700），元数据保持 0600；每次交给后续生命周期组件前重新核对内容、文件类型和权限。目录另有跨进程单写者锁、总字节/包数/扫描边界及保护 current/previous 的有界旧包清理。Stage/Verify 本身不执行制品，可执行位不构成沙箱或来源授权策略。

Host 与插件服务端显式将单条 gRPC 消息的收发上限设为 4 MiB，沿用当前 gRPC 默认边界，避免发送侧无显式上限。健康/错误诊断文本在两侧统一限制为最多 1024 个 UTF-8 字节，防止插件将大消息内容扩散进 Agent 状态报告。4 MiB 是初始安全上限，尚未用真实 Collector 描述及采集结果分布验证；调整前应补充 payload 测量和超限行为测试。

Linux 下 `ExecSupervisor` 每次启动在配置的私有运行目录中创建 `0700` 临时目录，只把 `HOMELAB_PLUGIN_ID`、`HOMELAB_PLUGIN_VERSION`、`HOMELAB_PLUGIN_SOCKET` 三个环境变量交给独立子进程。生产构造要求绝对路径、可执行且不允许 group/other 写入的 `plugin-launcher`，其直接父目录也不能由 group/other 写入或是符号链接；launcher 不经过 Shell，在执行已复验制品前设置硬 `RLIMIT_NOFILE`、`RLIMIT_NPROC`、`RLIMIT_FSIZE`、`RLIMIT_AS`、`RLIMIT_CPU` 和 `no_new_privs`。关闭时先向独立进程组发送 `SIGTERM`，调用方截止时间到达后发送 `SIGKILL`，进程退出后清理运行目录。`RLIMIT_NPROC` 按真实 UID 共享计数，其余限制主要按单进程或单文件生效，不能替代 cgroup 对整个进程树的 CPU 百分比、RSS 和任务数总量控制；实现也不切换 UID 或限制文件系统、系统调用和网络，同 UID 插件间仍不构成安全隔离。

Supervisor 在进程启动后、返回句柄前再次检查启动上下文；若 deadline 在 `exec.Start` 成功期间到期，会终止新进程并等待清理完成，避免将已超时的启动作为成功交付。

Agent 插件对账在每个显式控制轮询中检查已管理插件的健康状态。升级后活动记录保留一个前序已应用包；健康检查失败时先关闭当前进程，再尝试启动该前序包并报告其实际 Applied Revision；没有前序包时才重启当前包。即使恢复启动成功，这次健康失败仍计入连续失败并执行退避，避免反复崩溃的插件通过成功拉起绕过熔断。启动、下载、校验或健康恢复连续失败时，使用必填配置的 `max_consecutive_failures`、`initial_failure_backoff` 和 `max_failure_backoff` 执行指数退避，到达上限后将该 Desired Revision 标记为 Quarantined；若同一 Desired Revision 的插件仍活动，会尝试停止它，停止失败则报告 Degraded；若旧版本仍在活动则保留它并报告 Degraded。新的 Desired Revision 清除隔离记录。已观察意图、当前和单个前序包关联、退避截止及隔离状态写入显式条目/字节上限内的私有原子状态文件；文件绑定设备身份、校验和及单进程锁，损坏、身份不符、越界或权限过宽时拒绝启动。Agent 重启不自动启动旧插件；首次收到 Server Desired 后才复验暂存包并恢复允许运行的当前版本，回滚包在实际使用时同样复验。同一 Revision 的 Quarantined 状态不会因重启清除。该机制不创建独立监控定时器，进程故障发现时延受既有插件控制轮询间隔影响。

`PreparedPlugin.Close` 不在生命周期锁内等待插件 RPC：它先拒绝新调用、关闭 Host 连接以取消在途 RPC，再按调用者 deadline 请求 Supervisor 停止进程。若进程停止失败，句柄保留供调用方重试；此逻辑不替代 Supervisor 的进程终止和资源限制实现。

Manifest 权限项采用 `scope:capability` 小写标识并拒绝重复项。PluginManager 必须注入本机 `PluginAuthorizer`；每个包都须有匹配 plugin ID 与 publisher 的 grant，且 Manifest 中声明的每项权限都须获批，否则不会启动进程。该检查只负责执行前授权决策；系统调用、文件和网络能力仍须由 OS Supervisor 实际限制。
