Always respond in Chinese.

The `tofi-dev` skill has full project context. It will load automatically when working on this codebase.

## 项目现状（2026-04-03 更新）

**版本：** v0.5.0（latest release）| **分支：** master
**部署：** OCI ARM64 单二进制（不用 Docker），Caddy 反代，域名 tofi.sentiosurge.com
**端口：** TOFI_PORT 环境变量（默认 8321）
**构建：** `cd tofi-core && go build ./cmd/tofi/`
**前端：** tofi-web（Next.js 16），PM2 运行，`bash scripts/deploy.sh` 部署

### 版本历史
- **v0.3.1**: tofi doctor 健康检查 + 自动修复 + 启动 preflight
- **v0.4.0**: 删除遗留 engine（-5900行）+ ToolRegistry + Hooks 全接入 + AgentState 状态机
- **v0.5.0**: App webhook trigger + abort + stats API + tofi_sub_agent + tofi_schedule + tofi_task_list/stop + 17 工具集成测试 + App API 文档

### 产品定位

**Tofi = AI Agent as a Service**

开发者路径：创建 App → 配置 Prompt + Skills → 得到 Webhook URL → POST 调用 → AI 处理后返回结果
业务用户路径：同上，但通过 Telegram/Slack/Web 交互使用

### 已完成
- **Agent Harness**：20 个内置工具、ToolRegistry + deferred tool search、AgentState 状态机、6 个 Hooks、sub-agent、API retry + 模型降级、context 压缩三级、Shell AST 安全检测
- **App API**：完整 CRUD + webhook trigger + abort + stats + schedule 管理（18 个端点，文档在 `docs/APP_API.md`）
- **前端 (tofi-web)**：Chat（streaming + tool calls + thinking）、App 管理（Overview + Runs + Settings + 创建）、AI Key 设置
- **基础设施**：JWT 多租户、API Key 认证（`tofi-sk-*`）、spend cap + rate limiting、Doctor 健康检查
- **通知**：Telegram/Slack/Discord 自动推送（后端完成，未端到端验证）
- **Skills**：文件系统优先、deferred loading、系统技能嵌入二进制

### 待做（DX First 路线 — CEO Review 2026-04-03）
1. **更新文档**（本项 ← 正在做）
2. **Developer Quickstart 教程** — 5 分钟上手
3. **App Template Gallery** — 5-10 个预建模板
4. **Web 注册流程** — 邮箱验证 + 自动创建用户 + API Key
5. **定价页** — Free / Pro / Pay-as-you-go

### Deferred
- TypeScript/Python SDK（有 REST API 足够，SDK v2 做）
- Connectors 页面（Telegram/Slack 配置 UI）
- PostgreSQL 迁移（100+ 用户后）
- App Marketplace

### 架构决策
- **不用 Docker** — 单二进制部署，沙箱靠软防护
- **不做 billing system** — 先做成本日志 + 硬 spend cap
- **App Run 每次独立 session** — 跨 run 连续性通过 memory 实现
- **TTL 清理用 goroutine ticker** — 零外部依赖
- **遗留 workflow engine 已删除** — 所有执行走 Chat Session + Agent Loop
- **ToolRegistry 统一管理工具** — 不再用 extraHandlers map
- **AgentState 状态机驱动循环** — 不再用 for step

## 第一性原则

不要假设用户永远非常清楚自己想要什么和知道该如何得到。保持谨慎。从原始需求和问题出发：
- 如果目标不清晰，停下来和用户讨论
- 如果目标清晰但不是最短路径，告诉用户并建议更好的办法

## 方案规范（严格执行）

1. **禁止兼容性/补丁性方案** — 不写兜底代码、降级逻辑、fallback 策略。要么正确实现，要么不实现。
2. **禁止过度设计** — 用户需求之外的功能、预防性代码、"以防万一"的逻辑，一律不加。
3. **最短路径实现** — 从需求到代码的最直接路径。不绕弯、不加中间层、不做未被要求的抽象。
4. **不堆砌修复** — bug 修不好就找根因，不要在外面包一层又一层。
5. **不自作主张** — 用户没提的需求不做。

## Claude Code 源码参考

Claude Code 源码位于 `/Users/jackzhao/Developer/sentiosurge/claude-code-leak/src`（TypeScript），可作为 agent 架构参考。

**参考文档**：[`references/claude-code-patterns.md`](skills/tofi-dev/references/claude-code-patterns.md)
