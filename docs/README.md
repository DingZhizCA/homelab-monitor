# Homelab Monitor · 架构文档总索引

> 整理日期：2026-10-17。规划文档，不代表现有代码已实现；不会覆盖现有 AGENTS.md 和旧版 docs。

## 阅读顺序

**先读** `00-governance/01-decision-register.md` → `00-governance/02-nonnegotiables.md` → `01-architecture/01-system-overview.md`。之后按要实施的模块查阅。

## 文档状态约定

- **已确认**：用户在历轮访谈明确选择。
- **本轮按推荐采用**：用户本次授权“全部按你推荐的来”，纳入基线，但此前未逐项单独确认。
- **待实施核实**：当前数据源、协议和安全能力尚未通过目标设备实测。
- **业务待细化**：指标周期、告警阈值、页面组件默认值等实施时再确认。

## 目录索引

### 00 · 决策与约束

- [架构决策台账与批准状态](00-governance/01-decision-register.md)  ·  [Word](00-governance/01-decision-register.docx)
- [核心原则与不可违反约束](00-governance/02-nonnegotiables.md)  ·  [Word](00-governance/02-nonnegotiables.docx)
- [业务细节待实现清单](00-governance/03-deferred-business-backlog.md)  ·  [Word](00-governance/03-deferred-business-backlog.docx)

### 01 · 总体架构

- [系统总体架构与数据流](01-architecture/01-system-overview.md)  ·  [Word](01-architecture/01-system-overview.docx)
- [Go 模块边界与依赖约束](01-architecture/02-module-boundaries.md)  ·  [Word](01-architecture/02-module-boundaries.docx)
- [部署拓扑与系统依赖](01-architecture/03-deployment-topology.md)  ·  [Word](01-architecture/03-deployment-topology.docx)
- [Agent—Server 通信与配置一致性](01-architecture/04-agent-server-control-plane.md)  ·  [Word](01-architecture/04-agent-server-control-plane.docx)

### 02 · 插件与采集

- [Collector 插件接口与 SDK 契约](02-plugins/01-plugin-contract.md)  ·  [Word](02-plugins/01-plugin-contract.docx)
- [Plugin Host、运行时与故障隔离](02-plugins/02-plugin-host-runtime.md)  ·  [Word](02-plugins/02-plugin-host-runtime.docx)
- [插件分发、安装、升级与回滚](02-plugins/03-plugin-distribution.md)  ·  [Word](02-plugins/03-plugin-distribution.docx)
- [插件授权、沙箱与资源配额](02-plugins/04-plugin-trust-and-resources.md)  ·  [Word](02-plugins/04-plugin-trust-and-resources.docx)

### 03 · 数据与流量

- [指标契约、采集流水线与数据质量](03-data/01-metrics-and-pipeline.md)  ·  [Word](03-data/01-metrics-and-pipeline.docx)
- [存储、分层聚合与配额架构](03-data/02-storage-architecture.md)  ·  [Word](03-data/02-storage-architecture.docx)
- [SQLite 驱动候选评估记录](03-data/03-sqlite-driver-evaluation.md)
- [设备身份、拓扑与时间化关联](03-data/03-device-identity-and-topology.md)  ·  [Word](03-data/03-device-identity-and-topology.docx)
- [全网流量归属、方向与去重架构](03-data/04-network-flow-attribution.md)  ·  [Word](03-data/04-network-flow-attribution.docx)

### 04 · 安全与信任

- [身份认证、权限与信任边界](04-security/01-authentication-and-trust.md)  ·  [Word](04-security/01-authentication-and-trust.docx)
- [Newifi D2 远程只读采集安全边界](04-security/02-openwrt-readonly-collection.md)  ·  [Word](04-security/02-openwrt-readonly-collection.docx)

### 05 · Web 与 Widget

- [Web 导航、页面架构与访问体验](05-web/01-web-shell-and-navigation.md)  ·  [Word](05-web/01-web-shell-and-navigation.docx)
- [Widget、仪表盘发布与布局版本架构](05-web/02-widget-dashboard-publication.md)  ·  [Word](05-web/02-widget-dashboard-publication.docx)

### 06 · 告警与通知

- [告警引擎、规则版本与通知架构](06-alerting/01-alert-rule-incident-notification.md)  ·  [Word](06-alerting/01-alert-rule-incident-notification.docx)

### 07 · 运行与性能

- [资源预算、故障隔离与验收框架](07-operations/01-resource-budgets-and-safety.md)  ·  [Word](07-operations/01-resource-budgets-and-safety.docx)
- [交付、升级、部署及回滚边界](07-operations/02-release-rollout-and-compatibility.md)  ·  [Word](07-operations/02-release-rollout-and-compatibility.docx)

### 08 · 实施计划

- [分阶段实施路线与验收关卡](08-implementation/01-roadmap-and-gates.md)  ·  [Word](08-implementation/01-roadmap-and-gates.docx)

## 当前仓库整合说明

本交付包是将 `docs/` 下列目录**新增**到现有 Git 仓库使用的规划资料。原有 `docs/collector-design.md`、`docs/comment-guidelines.md`、`docs/phase-1-spec.md`、`docs/systemd-deployment-guide.md` 及根目录 `AGENTS.md` 不在此包内，解压时不应删除或覆盖这些旧文档；需要在实现阶段核对并更新其适用性。

## 边界声明

不包含可执行配置变更、systemd 命令、路由/防火墙规则、密钥或 Agent 插件二进制。
