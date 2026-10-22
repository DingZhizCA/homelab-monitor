# Home Lab Monitor

面向 Linux 家庭实验室的低功耗实时监控系统，包含 Go Agent、Go Server 和 React Dashboard。

## 当前能力

- 采集 CPU、内存、系统负载、运行时间、文件系统和网络指标
- Agent → Server 使用版本化 HTTP/JSON 协议
- Server 在内存中保存每台设备的最新快照；持久化批量模式可在重启时从 SQLite 恢复
- 提供 REST 初始状态与 SSE 实时更新
- 根据实际采样间隔计算 `online`、`stale`、`offline`
- 无活跃页面时使用 60 秒节能采样，页面活跃时切换到 5 秒实时采样
- 页面停止心跳后保留实时模式 30 分钟
- CPU 传感器和磁盘容量独立降频，并保留实际采集时间
- HDD 常规采集不会主动唤醒磁盘；用户可以显式请求主动刷新
- React、TypeScript、Tailwind CSS、shadcn/ui 与 Apache ECharts 响应式 Dashboard

架构决策、模块边界和阶段状态见 [架构文档索引](docs/README.md) 与[实施路线图](docs/08-implementation/01-roadmap-and-gates.md)。指标协议和低功耗约束见[指标流水线说明](docs/03-data/01-metrics-and-pipeline.md)。

## 环境要求

- Linux（主要目标为 Ubuntu Server）
- Go 1.25 或兼容版本
- Node.js 20.19+ 与 npm
- Agent 和 Server 正常运行均不需要 root

## 构建与验证

```bash
make build
make build-web
make check
```

也可以直接使用 Go：

```bash
mkdir -p bin
go build -trimpath -o bin/homelab-server ./cmd/server
go build -trimpath -o bin/homelab-agent ./cmd/agent
go build -trimpath -o bin/homelab-plugin-pack ./cmd/plugin-pack
go test -race ./...
go vet ./...
```

`make check` 还会运行 Web lint/测试及独立 SQLite 候选评测模块。评测模块不属于 Server/Agent 运行时依赖，也不会据此自动选择生产存储。

## 本地运行

终端一：

```bash
./bin/homelab-server --listen=:8080
```

终端二：

```bash
cp configs/agent.example.yaml configs/agent.local.yaml
./bin/homelab-agent --config=./configs/agent.local.yaml
```

终端三：

```bash
cd web
npm install
npm run dev
```

访问 `http://127.0.0.1:5173`。Vite 开发服务器会把 `/api` 转发到本机的 Go Server。

生产构建可以直接由 Go Server 提供：

```bash
cd web && npm run build && cd ..
./bin/homelab-server --listen=:8080 --web-dir=./web/dist
```

此时访问 `http://127.0.0.1:8080`，非 API 路径会回退到 Dashboard 的 `index.html`。

健康检查：

```bash
curl http://127.0.0.1:8080/api/v1/health
```

## 配置

开发环境可复制 [configs/agent.example.yaml](configs/agent.example.yaml)。生产环境默认配置路径为 `/etc/homelab-monitor/agent.yaml`。

关键采样参数：

```yaml
monitoring:
  idle_interval: 60s
  active_interval: 5s

collectors:
  sensor_interval: 1m
  disk_interval: 5m
  # 仅在确认是安全的 ATA/SATA 直连磁盘后显式开启。
  power_state: false
  power_state_interval: 1m
```

`device_id` 必须由管理员显式配置，不得从 IP 地址生成。真实 token 和本机配置不得提交到 Git。

Server 可通过 `--tls-cert`、`--tls-key` 启用 HTTPS，并通过 `--agent-credentials` 加载逐设备 Token 摘要。未配置 `--agent-credentials` 时，Server 为兼容旧协议不强制 Agent Token 或 HTTPS；该模式不提供 Agent 身份认证。配置格式和迁移边界见[安全说明](docs/04-security/01-authentication-and-trust.md)。

持久化批量指标入口默认关闭。启用时必须同时显式提供 `--storage-dir`、`--metrics-batch-max-snapshots` 和 `--metrics-batch-max-body-bytes`，并配置上述 HTTPS 与逐设备凭据。`--storage-dir` 必须是 Alienware 本地 SSD 上仅 Owner 可访问的目录；程序会在其中创建 `core.db` 和 `metrics.db`。只要启用存储，还必须提供正值的 `--storage-checkpoint-interval`、`--storage-checkpoint-wal-bytes`、`--storage-checkpoint-timeout`、`--storage-retention-interval`、`--storage-raw-retention`、`--storage-receipt-retention`、`--storage-retention-delete-limit`、`--storage-retention-timeout`、`--storage-core-max-bytes`、`--storage-metrics-soft-bytes`、`--storage-metrics-hard-bytes` 和 `--storage-filesystem-min-bytes`；凭据保留窗口必须长于原始指标窗口，指标软阈值必须低于硬阈值。参数不完整时 Server 拒绝启动，不会自行选择维护周期、保留期限、操作预算、容量或批次默认值。

人类用户认证和指标查询也默认关闭。启用时必须提供 `--storage-dir`、至少一个 `--auth-origin`，以及正值的 `--auth-session-ttl`、`--auth-login-max-keys`、`--auth-login-max-attempts`、`--auth-login-window`、`--history-max-range` 和 `--history-max-points`；同时必须配置 HTTPS。参数不完整时 Server 拒绝启动，不会采用隐式 TTL、限速或历史查询预算。启用后，Owner 与 Viewer 均可读取现有内建当前值和历史指标；账户变更和主动磁盘刷新仍要求 Owner。再提供正值的 `--dashboard-max-body-bytes` 会挂载持久化 Dashboard 路由；共享草稿的创建、保存、发布、回滚和审计在 `core.db` 同一事务提交，未配置时该路由保持关闭。

告警规则管理同样默认关闭。完整启用人类认证后，再显式提供正值的 `--alert-rule-max-body-bytes`、`--alert-rule-max-depth` 和 `--alert-rule-max-conditions`，Server 才会挂载 Owner 规则路由。规则 Revision、当前指针及审计事件持久化到 `core.db`；这些参数只限制请求和表达式复杂度，不提供任何隐式阈值、规则模板或 Incident 策略。告警评估另由 `--alert-evaluation-enabled` 显式开启，并要求正值的 `--alert-evaluation-interval`、`--alert-evaluation-timeout`、`--alert-evaluation-max-rules`、`--incident-max-states`，以及明确给出 `--incident-pending-for`、`--incident-recovery-for`、`--incident-unknown-behavior` 和 `--incident-disabled-behavior`；立即转换必须写成 `0s`。调度器以稳定游标跨轮分页，只读取 Server 当前快照；单轮串行且有界，失败按规则隔离并记录，不触发采集。

Agent 插件控制循环默认关闭。启用前必须显式配置插件公钥、权限 grant 文件、私有 staging/runtime 目录、协议版本、单制品大小上限、独立控制轮询间隔和操作超时；该循环与指标采样周期独立。Server 端还必须完整启用 SQLite、人类认证、设备认证，并提供现有私有绝对路径 `--plugin-artifact-dir` 和正值 `--plugin-max-artifact-bytes`。缺少任一依赖时插件控制路由不会启动。

`plugin-pack` 可用可选的 `--repository` 将刚签名且保留在 `--output` 中供审查的 bundle 原子导入 Server 制品仓库；重复导入相同字节幂等，冲突字节拒绝。导入制品不会自动创建或启用 Desired State。

Agent 的 `metrics_batch` 也默认关闭。启用时必须使用 HTTPS 和设备 Token，并显式设置队列条数、队列字节数、单批条数与请求体字节上限。队列仅驻留内存，失败时保留 pending 批次供后续采样轮次重试，容量满则拒绝新样本；它不会提高采样频率。Server 按上一段要求显式配置后会挂载持久化批量路由。Server 按采样时间分批删除超过原始窗口的快照，再按接收时间删除已经没有快照且超过更长窗口的批次凭据。每个 SQLite 连接应用显式的 `core.db` 与 `metrics.db` 页数硬上限；新指标写入前检查逻辑占用和文件系统保留空间。达到硬阈值时先执行一次有界清理和 PASSIVE checkpoint，仍不安全则返回 HTTP 507，Agent 保留 pending 批次。分层聚合和物理空间回收仍未实施。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/v1/health` | 健康检查 |
| `POST` | `/api/v1/metrics` | Agent 上报指标并接收控制指令 |
| `POST` | `/api/v1/metrics/batch` | 可选：接收认证的幂等指标批次；容量无法恢复时返回 507 |
| `GET` | `/api/v1/devices` | 设备列表和最新状态 |
| `GET` | `/api/v1/devices/{device_id}` | 单台设备最新状态 |
| `GET` | `/api/v1/events` | SSE 实时指标流 |
| `POST` | `/api/v1/presence` | 可见 Dashboard 心跳 |
| `POST` | `/api/v1/devices/{device_id}/refresh` | 排队一次主动磁盘刷新 |
| `GET` | `/api/v1/refresh/{request_id}` | 查询刷新任务状态 |
| `POST/GET` | `/api/v1/auth/*` | 可选：首次 Owner、登录、Session、注销和 Owner 账户管理 |
| `GET` | `/api/v1/dashboard/metrics/descriptors` | 可选：读取内建指标描述符 |
| `GET` | `/api/v1/dashboard/metrics/latest` | 可选：读取 Server 内存中的最新指标 |
| `GET` | `/api/v1/dashboard/metrics/history` | 可选：显式启用认证和历史预算后读取 SQLite 中的有界历史指标 |
| `GET/POST/PUT` | `/api/v1/dashboards*` | 可选：认证用户管理持久化个人 Dashboard，Owner 管理共享草稿、发布历史和回滚 |
| `GET/POST` | `/api/v1/agent/plugins/*` | 可选：设备认证的 Desired State 拉取、Reported State 上报和制品下载 |
| `PUT` | `/api/v1/owner/plugins/desired` | 可选：Owner 发布带原子审计的插件期望状态 |
| `GET/PUT` | `/api/v1/owner/alert-rules` | 可选：Owner 列出或按 CAS 保存 SQLite 持久化规则 |
| `GET/POST` | `/api/v1/owner/alert-rules/revisions` | 可选：由 Owner 查询不可变规则历史或以新 Revision 回滚 |

主动磁盘刷新可能唤醒 HDD，因此必须明确授权：

```bash
curl --request POST \
  --header 'Content-Type: application/json' \
  --data '{"collectors":["disk"],"allow_wake":true}' \
  http://127.0.0.1:8080/api/v1/devices/homelab-node/refresh
```

接口返回 `request_id`，可查询 `queued`、`delivered`、`completed`、`failed` 或 `expired` 状态。

## 最低功耗与 HDD 安全

常规采样只被动读取内核计数器。对于旋转磁盘：

- 没有业务 I/O 时不执行 `statfs`
- 业务 I/O 正在进行时允许被动刷新容量
- 休眠期间返回缓存值及原始 `collected_at`
- 默认不周期性调用 SMART、`hdparm` 或外部采集命令
- 可选的 `power_state` 只对 SATA 直连 HDD 低频执行 `hdparm -C` 状态查询；
  不支持或权限不足时返回 `unknown`，且不影响其他指标
- 只有用户提交 `allow_wake: true` 才允许主动刷新

指标快照的 `timestamp`、Server 的 `received_at`、CPU 的 `sensor_collected_at` 和磁盘的 `collected_at` 表示不同阶段的真实时间，缓存值不会伪装成刚采集的数据。

## systemd 部署

示例单元位于 [deployments/systemd](deployments/systemd)。安装二进制、配置文件并创建非特权用户后，可启用服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now homelab-server homelab-agent
```

日志由 journald 管理：

```bash
journalctl -u homelab-server
journalctl -u homelab-agent
```

当前生产 Agent 以非特权 `homelab-monitor` 用户运行，并使用
`PrivateDevices=true`。因此即使配置启用了 `power_state`，生产 systemd 服务也
可能因为无法访问主机块设备而返回 `unknown`。不要通过让整个 Agent 以 root
运行或加入 `disk` 组来绕过这一权限边界；后续 Storage Health 阶段将设计最小
权限的硬件查询机制。

## 项目结构

仓库开发约束见 [AGENTS.md](AGENTS.md)，架构资料与当前实现状态见 [docs/README.md](docs/README.md)。

```text
cmd/                  Server、Agent、插件打包命令
internal/agent/       配置、采集、发送、批量上报和插件对账
internal/metrics/     共享指标协议、批次模型与校验
internal/server/      HTTP 路由、内存状态、认证/插件/Dashboard 模块边界
internal/plugins/     插件契约、制品验证、Host、Supervisor 与 Registry
internal/auth/        用户、密码和 Session 服务边界
internal/audit/       无秘密载荷的管理审计事件契约
internal/dashboard/   Dashboard 模型、查询和仓储边界
tools/storage-eval/   与运行时隔离的 SQLite 候选评测模块
configs/              安全的示例配置
deployments/systemd/  systemd 单元
docs/                 架构决策、设计与实施路线
web/                  React Dashboard
```

## 当前限制

- 兼容单快照接口只更新内存；只有持久化批量接口写入的设备快照会在 Server 重启时恢复。刷新任务仍为进程内状态，不会恢复。
- 持久化认证、Owner 管理、内建当前指标、原始历史查询和 Dashboard 管理必须通过完整显式参数启用；默认运行保持这些路由关闭。共享发布只允许引用当前内建 Descriptor，更细粒度设备/字段 ACL 和敏感操作再认证尚未实现。
- 告警规则的不可变历史、回滚和审计可持久化装配；有界评估调度可从 Server 当前快照推进带容量和时间戳 CAS 的 SQLite Incident 状态。规则删除和跨 Revision Incident 迁移、通知去重、静默和投递仍未实现。
- 插件 Desired/Reported State、审计和制品路由可由 `cmd/server` 显式装配；制品仓库尚无总配额与清理策略，Agent 回滚关联和隔离状态仍不持久。
- Agent 的有界批量队列与插件控制均默认关闭，启用插件仍需本机信任根、权限 grant 和运行参数全部有效。
- 原始指标已有 SQLite 持久化、显式 PASSIVE WAL checkpoint、有界时间保留清理及软硬容量保护；PASSIVE 不负责截断 WAL 文件。容量参数尚未在目标设备调优，分层聚合、空间回收、流量归属账本和完整 Incident/通知流程尚未实现。
- 插件进程当前为同 UID 子进程，缺少系统级 CPU、内存、文件和网络隔离，不能视为生产安全沙箱。
