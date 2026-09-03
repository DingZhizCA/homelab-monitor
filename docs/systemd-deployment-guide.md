# Home Lab Monitor：从 tmux 迁移到 systemd

本文用于在当前服务器上首次安装、启动和维护 `homelab-server` 与
`homelab-agent`。完成后不再需要使用 tmux 保持服务运行。

## 给新 Codex 会话的任务说明

在新会话中可以直接发送：

> 请读取 `docs/systemd-deployment-guide.md`，检查当前环境，然后协助我将
> Home Lab Monitor 从 tmux 迁移到 systemd。执行前保留现有 Agent 配置，
> 不要在输出中泄露 token；完成后检查两个服务、8080 端口和健康接口。

## 当前已知状态（2026-09-03）

- 项目目录：`/home/johnding/workspace/projects/homelab-monitor`
- `/usr/local/bin/homelab-server` 和 `/usr/local/bin/homelab-agent` 已存在
- Dashboard 已复制到 `/usr/local/share/homelab-monitor/web`
- systemd 单元尚未安装
- `homelab-monitor` 系统用户尚未创建
- `/etc/homelab-monitor/agent.yaml` 尚不存在
- 当前未检测到运行中的 Server 或 Agent

新会话必须重新检查这些状态，不能假设它们仍然不变。

## 1. 检查是否仍有 tmux 进程

```bash
tmux list-sessions
ps -eo pid,ppid,args | grep -E '[h]omelab-(server|agent)'
ss -ltnp | grep ':8080'
```

如果进程仍在运行，先确认对应 tmux 窗口，再在窗口中按 `Ctrl+C` 正常停止。
不要直接用模糊的 `pkill` 命令，以免停止错误进程。

## 2. 构建程序和 Dashboard

```bash
cd /home/johnding/workspace/projects/homelab-monitor
source /home/johnding/.nvm/nvm.sh

make build
make build-web
```

构建结果：

- `bin/homelab-server`
- `bin/homelab-agent`
- `web/dist/`

## 3. 创建专用系统用户

先检查用户是否存在：

```bash
getent passwd homelab-monitor
```

不存在时创建：

```bash
sudo useradd \
  --system \
  --no-create-home \
  --shell /usr/sbin/nologin \
  homelab-monitor
```

## 4. 安装二进制和网页

```bash
sudo install -m 755 bin/homelab-server /usr/local/bin/homelab-server
sudo install -m 755 bin/homelab-agent /usr/local/bin/homelab-agent

sudo mkdir -p /usr/local/share/homelab-monitor/web
sudo cp -a web/dist/. /usr/local/share/homelab-monitor/web/
sudo chown -R root:root /usr/local/share/homelab-monitor
sudo chmod -R a+rX /usr/local/share/homelab-monitor
```

网页目录必须允许 `homelab-monitor` 用户读取。不能保持为仅拥有者可读的
`0700` 权限，否则 Server 会因无法读取 `web-dir` 而启动失败。

## 5. 准备 Agent 配置

创建配置目录：

```bash
sudo install -d -m 750 -o root -g homelab-monitor /etc/homelab-monitor
```

如果以前在 tmux 中使用过另一份真实配置，应优先复制那一份，不要覆盖或
遗失其中的 `device_id`、Server 地址和 token。如果没有真实配置，再复制示例：

```bash
sudo cp configs/agent.example.yaml /etc/homelab-monitor/agent.yaml
sudo nano /etc/homelab-monitor/agent.yaml
```

Server 和 Agent 在同一台机器时，可以使用以下结构：

```yaml
server:
  url: "http://127.0.0.1:8080"
  token: ""

agent:
  device_id: "homelab-node"
  display_name: "Home Lab Node"
  interval: 5s

monitoring:
  idle_interval: 60s
  active_interval: 5s

collectors:
  cpu: true
  memory: true
  disk: true
  network: true
  sensor_interval: 1m
  disk_interval: 5m
```

设置权限：

```bash
sudo chown root:homelab-monitor /etc/homelab-monitor/agent.yaml
sudo chmod 640 /etc/homelab-monitor/agent.yaml
```

检查配置时不要把 token 打印到终端记录或聊天中。

## 6. 安装并启动 systemd 单元

```bash
sudo cp deployments/systemd/homelab-server.service /etc/systemd/system/
sudo cp deployments/systemd/homelab-agent.service /etc/systemd/system/

sudo systemctl daemon-reload
sudo systemctl enable --now homelab-server homelab-agent
```

## 7. 验证部署

检查服务：

```bash
sudo systemctl status homelab-server homelab-agent --no-pager
```

检查健康接口：

```bash
curl --fail http://127.0.0.1:8080/api/v1/health
```

检查设备是否上报：

```bash
curl --fail http://127.0.0.1:8080/api/v1/devices
```

检查端口：

```bash
ss -ltnp | grep ':8080'
```

查看最近日志：

```bash
sudo journalctl -u homelab-server -u homelab-agent -n 100 --no-pager
```

如果 Agent 刚启动，节能模式下设备数据最多可能等待约 60 秒更新。

## 8. 常用维护命令

重新启动：

```bash
sudo systemctl restart homelab-server homelab-agent
```

停止：

```bash
sudo systemctl stop homelab-server homelab-agent
```

启动：

```bash
sudo systemctl start homelab-server homelab-agent
```

实时查看日志：

```bash
sudo journalctl -fu homelab-server
sudo journalctl -fu homelab-agent
```

## 9. 后续代码更新流程

```bash
cd /home/johnding/workspace/projects/homelab-monitor
source /home/johnding/.nvm/nvm.sh

make build
make build-web

sudo install -m 755 bin/homelab-server /usr/local/bin/homelab-server
sudo install -m 755 bin/homelab-agent /usr/local/bin/homelab-agent
sudo cp -a web/dist/. /usr/local/share/homelab-monitor/web/
sudo chown -R root:root /usr/local/share/homelab-monitor
sudo chmod -R a+rX /usr/local/share/homelab-monitor

sudo systemctl restart homelab-server homelab-agent
curl --fail http://127.0.0.1:8080/api/v1/health
```

## 10. 故障排查

如果显示 `Unit ... not found`，说明服务文件尚未复制到
`/etc/systemd/system/`，执行第 6 节。

如果 Server 报 `web-dir must be a readable directory`，检查：

```bash
namei -l /usr/local/share/homelab-monitor/web
sudo -u homelab-monitor test -r /usr/local/share/homelab-monitor/web/index.html
```

如果 Agent 启动失败，检查配置是否存在且服务用户可读：

```bash
sudo -u homelab-monitor test -r /etc/homelab-monitor/agent.yaml
sudo journalctl -u homelab-agent -n 100 --no-pager
```

如果 8080 端口已被占用，先使用以下命令确认占用者，不要直接结束未知进程：

```bash
sudo ss -ltnp | grep ':8080'
```

## 低功耗说明

systemd 只负责托管进程，不会改变采集频率，也不会额外访问 HDD。Agent 仍然
遵守项目的最低功耗策略：页面无人访问时使用 60 秒采样，CPU 温度默认每分钟
读取一次，空闲 HDD 不执行 `statfs`、SMART 或硬件探测。只有用户在网页中明确
执行主动刷新，才允许一次可能唤醒 HDD 的容量读取。
