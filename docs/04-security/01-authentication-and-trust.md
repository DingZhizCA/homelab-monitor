# 身份认证、权限与信任边界

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 访问与角色
Web 仅通过家庭 LAN 与 Tailnet 访问，不直接暴露公网。局域网和 Tailscale 使用同一套 Homelab Monitor 本地账号和服务端 Session；首次初始化由本机创建 Owner，后续注册必须由 Owner 管理。第一版包含 Owner 与 Viewer：Viewer 能查看获授权汇总与个人布局，不可修改采样、存储、插件、凭据或查看敏感连接详情。

## 独立的三类身份
| 身份 | 职责 | 绝不能共用 |
|---|---|---|
| Human User | Web 登录、规则管理、Dashboard | Agent 凭据、SSH 密钥 |
| Agent Identity | N1/Alienware 上报、配置对账、插件下载 | Owner Session |
| Remote Credential | Server 到 Newifi 的限权读取 | 主机管理员通用密钥 |

## Web Session
推荐服务端 Session，密码用 Argon2id；浏览器采用 Secure、HttpOnly、SameSite Cookie，修改 API 防 CSRF、校验 Origin。登录速率限制、主动会话撤销、敏感操作再认证、设备会话列表、最后 Owner 保护都属于基础安全能力。跨不同 HTTPS Origin 不承诺 Cookie 自动共享，但账号数据库一致。

当前 `internal/auth` 提供 Argon2id 密码 verifier、Owner 原子初始化入口、Owner 创建账户、登录、当前角色解析和服务端 Session 撤销服务；Session 只向浏览器返回随机 Token，仓储仅接收 SHA-256 Token 摘要。未知或禁用账户的登录路径会执行同成本 dummy verifier；账户管理会复查调用者当前角色，禁用账户被解析时撤销其所有 Session。密码哈希使用随机盐 16 bytes、输出 32 bytes，成本参数为 19 MiB / 2 passes / parallelism 1，并严格拒绝其他 PHC 成本参数。该值达到 [OWASP Password Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) 的最低推荐基线；尚未在目标 Server 上压测。`internal/server.AuthHandler` 提供 HTTPS bootstrap/login/session/logout/user-create 路由；写操作校验显式允许的 Origin，Session Cookie 设为 Secure、HttpOnly、SameSite=Lax。登录限速器使用有界固定窗口实现，尝试数、窗口和键容量必须由调用方提供。`RouterOptions.Auth` 被显式注入时，设备读取与 SSE 需要有效用户 Session，磁盘主动刷新仅 Owner 可用。

Web 已有同源登录、首次 Owner 初始化和注销表单，以及 Session 检查：认证启用时，未登录页面不会请求设备 API、连接 SSE 或发送 Presence；Owner 创建后返回登录表单，登录成功后启动监控请求。Owner Session 可见的账户页调用 Owner 保护的账户创建、列表和状态修改 API，列表只返回 ID、用户名、角色和启用状态。角色或启用状态变更由用户仓储原子检查，始终保留至少一个启用 Owner；禁用账户的后续 Session 解析会拒绝并撤销其会话。若 Auth Handler 未注册，Server 返回认证 API 404，前端保持旧的无认证兼容路径。`internal/audit` 已定义不含自由文本和秘密载荷的最小管理事件；首次 Owner、账户创建、账户状态更新、插件 Desired State 发布、共享 Dashboard 管理和告警规则修订都要求对应仓储把事件与状态变更原子提交。认证中间件把已验证身份放入请求 Context。SQLite 用户/Session/账户审计、Dashboard 和告警规则仓储已经实现同事务审计、当前 Actor 复核、重开恢复、CAS 冲突及失败零写入测试；Incident 等其他业务域的生产审计、敏感操作再认证和 Viewer 字段级授权仍未实现。

`cmd/server` 现可在显式提供私有存储目录、至少一个 HTTPS Origin、Session TTL、登录限速键容量/次数/窗口以及历史最大范围/点数时创建并注入 AuthHandler；缺少任一参数都会拒绝启动，默认运行仍保持认证关闭。启用后设备读取、SSE、Presence、当前指标和历史指标要求有效人类 Session，主动磁盘刷新仍仅限 Owner。额外提供 Dashboard 请求体字节上限后，Server 注入 SQLite Dashboard 仓储、内建 Descriptor 发布策略和对应认证 Handler。ADR-020 允许有效 Owner 与 Viewer 读取现有内建指标，因此共享发布拒绝未知 Descriptor；查询时仍再次鉴权。更细粒度设备/字段 ACL 尚未实现。

Server 的认证、Dashboard、插件状态、磁盘刷新及指标写请求均使用有界严格 JSON 解码：对象重复键、未知字段和多余 JSON 值会被拒绝，避免重复认证字段、`allow_wake` 或修订字段按解析器的“最后值覆盖”行为产生歧义。

## Agent 网络认证（推荐）
TLS 强制验证 Server 身份；首版采用独立设备 Token（强随机、权限范围绑定、可撤销轮换），逐步支持 mTLS 的双向认证。一次性 enrollment token 仅用于设备初次获批注册。所有请求须授权到 Device ID 和相应 API，不允许枚举其他设备数据。

当前实现接受 Server 启动参数 `-tls-cert` 与 `-tls-key` 直接提供 HTTPS；二者必须成对出现。可选 `-agent-credentials` 加载设备校验文件，并且该选项要求 HTTPS。文件仅保存令牌 SHA-256 摘要，JSON 结构为 `{"devices":[{"device_id":"n1","token_sha256":"<64位小写十六进制>"}]}`；文件必须是非符号链接的普通文件，权限仅允许所有者访问，父目录不能由 group/other 写入，JSON 对象重复键会被拒绝。Agent 只在 `server.token` 配置非空时发送 Bearer Token，且配置校验要求 `server.url` 使用 HTTPS。指标和插件控制客户端都不自动跟随 HTTP 重定向，避免把凭据请求转交给其他地址。插件控制路由启用时复用逐设备凭据，并要求持久化人类认证、私有制品目录和显式单制品上限同时存在。

**当前兼容边界：**若 Server 未配置 `-agent-credentials`，指标 API 不强制设备 Token，也不要求 HTTPS；这是为既有本地运行协议保留的兼容模式，不能视为已启用 Agent 身份认证。部署到受信任网络之前应配置 HTTPS 与逐设备凭据；若要改成默认强制，需要先迁移现有 Agent 配置和凭据分发方式。

## Plugin 权限
插件系统本地 RPC、操作系统权限与 Server 控制权限彼此分离。原生独立进程不可视为绝对沙箱；危险插件需要严格来源授权、资源限制及最小文件/网络访问。插件无权直接获取用户 Session、Agent Token 或 Newifi 私钥。

当前代码的 `GrantAuthorizer` 要求每个插件包的 plugin ID 与 publisher 命中显式本机 grant；grant 可为空权限列表，以批准包身份但不授予额外能力。Manifest 声明的每项 capability 还必须是该 grant 的子集。`LoadGrantAuthorizer` 可从受保护 JSON 文件加载策略，格式为 `{"grants":[{"plugin_id":"network","publisher":"trusted.example","permissions":["read:proc"]}]}`。文件要求非符号链接普通文件、owner-readable 且 group/other 无权限，父目录不可由 group/other 写入，最大 1 MiB，未知字段、多余 JSON 值和对象重复键会拒绝。`LoadPublisherPublicKey` 从受保护文件读取原始 32-byte Ed25519 公钥；`NewPluginRuntime` 显式装配该信任根、本机 grants、Stager、受限 Supervisor、PluginManager、Reconciler 和 Agent HTTPS 控制客户端。`cmd/agent` 只有在 `plugins.enabled` 明确开启且 launcher、五项硬 rlimit 及其他安全/资源参数全部提供时才启动独立控制轮询；指标采样周期不受影响。launcher 在插件执行前设置 `no_new_privs`，插件只收到三个 Host 环境变量。Server 可显式装配持久化插件控制链，但本地测试不等于目标设备验收。授权门和基础 rlimit 不能替代不同 UID、cgroup、文件系统、系统调用及网络隔离；Owner 管理 UI 与这些 OS 能力限制仍未实现。

## 管理操作与审计
插件安装/升级、密钥轮换、规则发布/回滚、身份合并拆分、敏感诊断开启及历史删除需审计。日志不得记录密码、Token、SSH 私钥、完整 Push Endpoint 或敏感连接载荷。

审计不能由 Handler 在业务写入后另行追加，否则审计失败时状态已经不可回滚。当前账户、插件 Desired State、插件 Collection Policy、共享 Dashboard 和告警规则仓储契约因此要求生产适配器在同一事务内写入事件；事件只包含 UTC 时间、Actor ID、稳定 Action、资源类型和资源 ID。Collection Policy 启用写入还在同一事务复核绑定的插件 Desired Revision 仍启用；HTTP 前置校验确认 Collector 存在于对应签名 Manifest。共享 Dashboard 创建的 ID 由仓储分配，仓储须在提交前用该 ID 调用事件工厂，并将返回事件与 Dashboard 一起提交。上述对象均已在 `core.db` 中实现对应事务边界。审计事件不写入普通应用日志载荷。

## HTTPS 与现有服务
LAN 提供可信 HTTPS，Tailnet 利用受控 HTTPS 入口；具体域名证书和反向代理方案待实施核对。Alienware 既有 Tailscale Serve 443 为 CouchDB 服务的配置不能被新 Web 覆盖。PWA 推送接收不要求 Web 公网入站端口。
