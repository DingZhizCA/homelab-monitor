# 交付、升级、部署及回滚边界

> **文档状态**：架构规划（尚未实施）
> **整理依据**：用户历轮确认 + “未单独确认项按推荐方案生成”的授权
> **说明**：技术参数的具体默认数值需要在模块实施时验证；本文不构成现网变更指令。

## 与现有仓库关系
仓库 `homelab-monitor` 含 cmd/agent、cmd/server、internal/agent、internal/server、configs、deployments/systemd、web 及分层架构文档。规划目标不代表代码已经符合设计；实施前应重新检查实际仓库状态、未提交改动、现有 API 契约与 systemd 单元。

## 发布单位
| 发布单元 | 升级路径 |
|---|---|
| Go Server | Alienware 上独立版本、元数据迁移、重启验证 |
| Go Agent | N1/Alienware 分目标平台发布，先验证再切换 |
| Plugin Packages | 独立版本与签名，Server 管理 + Agent 本地拉取 |
| Web App | 与 Server API 兼容性协调发布 |
| 数据 Schema | 单独 version/migration + 可恢复备份 |

## 建议版本策略
API、Agent 配置、Plugin RPC、Metrics Schema、Dashboard Layout、Alert Rule 均独立有稳定版本；不将所有对象强行绑定同一软件版本。Server 至少在约定的短兼容窗口支持旧 Agent/插件，具体长度实施阶段决定。

## 渐进部署顺序
现网只读审核 → Alienware Server 基础能力 → 本机 Agent/Plugin Host → N1 Agent 安全隔离验证 → 插件生命周期与升级回滚 → Newifi 受限只读采集 → 数据归属与 Web/告警。每一步先验收再扩大覆盖。

## 回滚边界
插件回滚不能改变网络服务配置；Agent 回滚需保留安全的认证凭据和插件安装清单；Server 回滚涉及数据库 Schema 时必须确认迁移兼容。生产系统需具备停止监控服务后仍可正常代理和路由的独立回滚手册。

## 升级审计
安装、升级、拒绝、回滚、权限变化、规则语义变更都记录可追踪 Revision。Web 发布版本回滚与设备插件部署回滚是不同的控制流程，不应混用命令或状态。
