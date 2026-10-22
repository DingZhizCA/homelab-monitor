# Home Lab Monitor

面向 Linux 家庭实验室的低功耗实时监控系统，包含 Go Agent、Go Server 和 React Dashboard。

## 当前能力

- 采集 CPU、内存、系统负载、运行时间、文件系统和网络指标
- Agent → Server 使用版本化 HTTP/JSON 协议
- Server 在内存中保存每台设备的最新快照
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

Agent 插件控制循环默认关闭。启用前必须显式配置插件公钥、权限 grant 文件、私有 staging/runtime 目录、协议版本、单制品大小上限、独立控制轮询间隔和操作超时；该循环与指标采样周期独立。当前 Server 可执行文件尚未装配插件期望状态/制品路由，因此仅配置 Agent 端不足以启用插件分发。

Agent 的 `metrics_batch` 也默认关闭。启用时必须使用 HTTPS 和设备 Token，并显式设置队列条数、队列字节数、单批条数与请求体字节上限。队列仅驻留内存，失败时保留 pending 批次供后续采样轮次重试，容量满则拒绝新样本；它不会提高采样频率。当前 Server 可执行文件尚无生产历史写入器，也未挂载批量路由。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/v1/health` | 健康检查 |
| `POST` | `/api/v1/metrics` | Agent 上报指标并接收控制指令 |
| `POST` | `/api/v1/metrics/batch` | 可选：装配设备认证和原子历史写入器后接收幂等指标批次 |
| `GET` | `/api/v1/devices` | 设备列表和最新状态 |
| `GET` | `/api/v1/devices/{device_id}` | 单台设备最新状态 |
| `GET` | `/api/v1/events` | SSE 实时指标流 |
| `POST` | `/api/v1/presence` | 可见 Dashboard 心跳 |
| `POST` | `/api/v1/devices/{device_id}/refresh` | 排队一次主动磁盘刷新 |
| `GET` | `/api/v1/refresh/{request_id}` | 查询刷新任务状态 |
| `GET` | `/api/v1/dashboard/metrics/history` | 可选：在显式装配历史源、认证、授权和查询预算后读取有界历史指标 |
| `GET/PUT` | `/api/v1/owner/alert-rules` | 可选：装配认证和规则仓储后由 Owner 列出或按 CAS 保存规则 |

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

- `cmd/server` 当前只装配原有指标、设备、SSE、Presence 和磁盘刷新 API；设备快照与刷新任务保存在内存，Server 重启后会丢失。
- Dashboard 历史查询 Handler 已有可选路由实现，但 `cmd/server` 未装配历史存储和该路由；上表不表示当前运行实例已提供该接口。
- 认证、Owner 管理、插件控制、批量指标、Dashboard 查询等模块已有独立代码或测试，但依赖尚未选定的生产持久化和运行装配，不能据此视为当前 Server 可用功能。
- Agent 的有界批量队列已提供默认关闭的采样主循环装配；插件控制默认关闭，Server 命令仍未装配生产批量写入器、插件控制及制品路由。
- 尚无持久化历史指标、流量归属账本或完整 Incident/通知流程。SQLite 仍是评测候选，未接入运行依赖。
- 插件进程当前为同 UID 子进程，缺少系统级 CPU、内存、文件和网络隔离，不能视为生产安全沙箱。
