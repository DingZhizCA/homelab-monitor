# 插件分发、安装、升级与回滚

> **文档状态**：架构规划与当前实现边界
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 控制权原则
Server 保存 Plugin Registry、Artifact Repository 和目标设备 Desired State；Agent 主动拉取并执行本地安全校验；实际部署和回滚由本机 Plugin Host 完成。Agent 对资源或权限风险拥有拒绝权；控制平面不发送任意 Shell 命令。

## 制品仓库
按 `plugin_id/version/platform/digest` 标识不可变制品。Manifest、二进制、签名和元数据不可被同名原位覆盖；下载从 Server 的受认证端点发起，不要求 N1 对外监听插件管理端口。

Agent Desired State 中启用的版本必须携带 `artifact.platform` 与 `artifact.sha256`，使请求能唯一定位平台制品；Server 和 Agent 都拒绝缺失或格式不合法的制品身份。`FileArtifactRepository` 在调用方明确给出单制品、仓库总字节、包数量及扫描条目上限后，按 `plugin_id/version/os-arch/digest` 保存不可变的 Manifest、签名和二进制文件，并拒绝同一身份的不同字节覆盖。仓库用跨进程文件锁串行导入和清理，每次导入按磁盘实际内容重新核算；相同内容重试在满额时仍幂等，新包达到任一边界则拒绝，不在导入路径隐式删除。

Server 周期维护使用显式间隔、超时、单轮删除量和孤立宽限期。每轮先从 SQLite 读取所有启用 Desired State 的制品引用；读取或校验失败时不删除。候选必须是结构、权限、Manifest 路径和摘要均复验通过的完整包，早于宽限期且不在保护集合中。Owner Desired 发布从制品存在性检查到数据库提交与清理共享互斥锁，避免包在发布竞态中消失；下载与清理由仓库读写锁隔离。清理按目录时间和内容地址稳定排序，删除量和扫描条目有界，并清理空父目录及崩溃遗留的内部临时目录。上述容量只计算受管包文件字节，不替代文件系统整体保留空间监控。

Agent 将 `plugins.max_artifact_bytes` 显式传入 Stager；Stage 拒绝超限制品，VerifyStaged 对 Manifest、签名、摘要标记和二进制文件都做大小界限检查，并使用有界读取，避免损坏或异常暂存文件触发无界内存分配。该单制品限制不等于暂存目录总配额或过期清理。

`plugins.SignPackage` 提供显式调用的发行构建步骤：计算二进制摘要、验证 Manifest、生成规范 JSON，再用调用方传入的 Ed25519 私钥签名并返回 ArtifactKey。它不加载或保存私钥，不负责制品大小限制或发布审批；发行流水线必须在受控环境管理私钥。

仓库内 `cmd/plugin-pack` 将该流程封装为本地工具。调用者显式传入 Manifest 模板、构建产物、私钥文件、输出路径和最大产物字节数；私钥必须是权限不向 group/other 开放的可读普通文件，父目录不得由 group/other 写入，工具通过受控目录句柄打开并核对文件身份。输出路径必须不存在，生成的 bundle 目录为 `0700`、内部文件为 `0600`。可选 `-repository` 指向现有私有绝对路径，将同一 bundle 通过 `FileArtifactRepository.Put` 原子导入 Server 内容寻址仓库；此时必须同时提供与 Server 相同的仓库总字节、包数量和扫描条目上限。本地输出仍保留供审查。工具不启动产物，也不创建或启用 Desired State。示例：

```sh
go run ./cmd/plugin-pack \
  -manifest ./dist/manifest.json \
  -artifact ./dist/plugin-linux-arm64 \
  -private-key /protected/path/publisher.ed25519 \
  -output ./dist/signed-bundle \
  -repository /private/server/plugin-artifacts \
  -max-artifact-bytes "$MAX_ARTIFACT_BYTES" \
  -repository-max-bytes "$MAX_REPOSITORY_BYTES" \
  -repository-max-packages "$MAX_REPOSITORY_PACKAGES" \
  -repository-max-scan-entries "$MAX_REPOSITORY_SCAN_ENTRIES"
```

执行前需由发行环境设置四个变量为与 Server 一致的已批准容量边界。

受认证的 Agent 下载端点使用 HTTPS，并以 multipart/form-data 返回 `manifest.json`、`manifest.sig` 和 `artifact` 三个部分。设备只能读取自己当前启用 Desired State 指向的确切制品。Agent 客户端限制响应体和各部分大小，校验规范 Manifest、制品摘要、版本和平台；`plugins.Reconciler` 在暂存前再次检查包身份，再由 Stager 使用本机信任根验签，并由 PluginManager 准备进程、检查协议和健康状态。升级先停止旧实例；新版本启动失败时尝试重新准备原版本，禁用状态会停止对应进程。控制器只处理显式传入的状态批次，不创建轮询定时器或改变指标采样周期。

仓库、Handler、客户端和可注入 Reconciler 都有本地测试；Agent—Server 的集成测试覆盖签名制品下载及发行方验签。`PluginAdminHandler` 提供 Owner Desired State 写入口：仅在 Router 注入 Auth Handler 时挂载，并通过 HTTPS、允许的 Origin 和 Owner 会话校验；启用目标在发布前必须已存在于制品仓库。Handler 从认证 Context 取得 Owner ID，SQLite `PluginRepository` 在同一事务中复查当前 Owner、提交新 Desired Revision 和无秘密载荷的审计事件；幂等重试不重复记录，校验、制品或修订冲突失败不提交状态或成功事件。Reported 批次也先完成全部校验与旧 Revision 检查再原子提交。`cmd/server` 只有在 SQLite、人类认证、设备认证、私有制品目录、全部制品容量边界及有界清理参数显式配置时装配控制、制品与 Owner 路由；默认保持关闭。

## 升级过程
1. Owner 在 Server 发起插件安装或版本升级申请，Server 进行基础资格预检并生成 Desired Revision。
2. N1 Agent 轮询到该计划，核验设备标识、签名信任根、平台架构、协议与指标兼容、必要磁盘空间和系统预算。
3. 插件包下载至 staging 区，不运行未经验证的二进制；验证失败停止流程并保留旧版本。
4. 在安全窗口中停用旧实例并启用新实例；原则上不让同一 Collector 的两个版本同时生产指标。
5. 新实例完成握手、Describe、首次有界采集和资源校验后提交 `applied_revision`。
6. 失败则停止新实例并尝试回滚旧版本。若回滚失败，隔离插件并记录错误，不重启 sing-box/Tailscale。

## 卸载与数据保留
卸载插件不立即清理历史指标、告警、审计及设备身份记录；删除配置中的秘密引用但留受保护的审计元数据。过去依赖被卸载 Collector 的规则进入不可评估状态。

## 网络中断
Server 暂时不可达时本机已批准版本不自动升级；重复计划幂等；取消或新 Revision 不得由乱序回应覆盖；Agent 可保留最近可用安装包和状态，但不建立大规模历史指标队列。

## 平台与供应链
Alienware x86-64、N1 ARM64 需分别匹配制品。签名公钥由本机可信环境独立管理；Server 目录摘要不能替代签名验证。第一版不承诺在线安装任意来源未经审查的第三方插件。
