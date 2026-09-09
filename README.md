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

Phase 1 的完整范围和非目标见 [docs/phase-1-spec.md](docs/phase-1-spec.md)，采集算法与功耗约束见 [docs/collector-design.md](docs/collector-design.md)。

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
go test -race ./...
go vet ./...
```

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

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/v1/health` | 健康检查 |
| `POST` | `/api/v1/metrics` | Agent 上报指标并接收控制指令 |
| `GET` | `/api/v1/devices` | 设备列表和最新状态 |
| `GET` | `/api/v1/devices/{device_id}` | 单台设备最新状态 |
| `GET` | `/api/v1/events` | SSE 实时指标流 |
| `POST` | `/api/v1/presence` | 可见 Dashboard 心跳 |
| `POST` | `/api/v1/devices/{device_id}/refresh` | 排队一次主动磁盘刷新 |
| `GET` | `/api/v1/refresh/{request_id}` | 查询刷新任务状态 |

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

代码注释和文档注释统一遵循
[Home Lab Monitor 代码注释规范](docs/comment-guidelines.md)。该规范适用于 Go、
TypeScript/React、配置、脚本和样式文件。

```text
cmd/                  两个可执行程序入口
internal/agent/       配置、采集、发送和低功耗策略
internal/metrics/     共享协议类型与校验
internal/server/      状态、REST、SSE、presence 和刷新任务
configs/              安全的示例配置
deployments/systemd/  systemd 单元
docs/                 规范与设计文档
web/                  React Dashboard
```

## 当前限制

- Phase 1 只保存最新状态，Server 重启后数据和刷新任务会丢失
- 没有持久化历史指标
- 没有用户系统或多租户功能
- 浏览器实时曲线只保存当前会话最近约五分钟的数据
