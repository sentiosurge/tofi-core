# Tofi Action Library

官方内置 Action 库,编译进二进制,提供开箱即用的常用功能。

## 可用 Actions

### 1. `tofi/ai_response` - AI 响应生成

使用 OpenAI API 生成 AI 响应 (默认使用 Tofi 官方 Token,支持最新的 GPT-5.1)。

**必需参数:**
- `prompt` - 用户问题/提示词

**可选参数:**
- `system_prompt` - 系统提示词 (默认: "你是一个专业的 AI 助手。请根据用户的问题提供准确、有帮助的回答。")
- `model` - 使用的模型 (默认: "gpt-5.1" - OpenAI 最新的旗舰模型)

**支持的模型:**
- `gpt-5.1` - 最新旗舰模型 (默认,使用 Responses API)
- `gpt-5.1-codex-max` - 专为编程任务优化
- `gpt-4.1-2025-04-14` - GPT-4.1 系列
- `gpt-4-turbo` - GPT-4 Turbo

**示例 1 - 基础使用:**
```yaml
nodes:
  ask_ai:
    type: workflow
    config:
      action: "tofi/ai_response"
    input:
      prompt: "请解释什么是 Kubernetes?"
```

**示例 2 - 自定义系统提示词和模型:**
```yaml
nodes:
  code_review:
    type: workflow
    config:
      action: "tofi/ai_response"
    input:
      prompt: "请 review 这段代码:\n\n{{previous_step.code}}"
      system_prompt: "你是一个专业的代码审查专家。请提供详细的代码质量分析和改进建议。"
      model: "gpt-4"
```

---

### 2. `tofi/webhook_notify` - Webhook 通知

发送 HTTP Webhook 通知,支持自定义 URL 和 payload。

**必需参数:**
- `url` - Webhook URL
- `method` - HTTP 方法 (GET/POST/PUT 等)
- `payload` - JSON payload (字符串或对象)

**示例:**
```yaml
nodes:
  notify_slack:
    type: workflow
    config:
      action: "tofi/webhook_notify"
    input:
      url: "https://hooks.slack.com/services/xxx"
      method: "POST"
      payload: '{"text": "Hello from Tofi!"}'
```

---

### 3. `tofi/read_file` - 读取文件

读取文本文件内容,支持各种文本格式。

**必需参数:**
- `path` - 文件路径 (相对或绝对路径)

**返回值:**
- 成功: 文件的完整内容
- 失败: 错误信息 (文件不存在、无权限等)

**示例:**
```yaml
nodes:
  read_config:
    type: workflow
    config:
      action: "tofi/read_file"
    input:
      path: "config/settings.yaml"
    next: ["process_config"]

  process_config:
    type: shell
    input:
      script: |
        echo "Config content:"
        echo "$CONFIG"
    env:
      CONFIG: "{{read_config.read}}"
    dependencies: ["read_config"]
```

---

### 4. `tofi/slack_notify` - Slack 通知

发送消息到 Slack 频道 (基于 Webhook)。

**必需参数:**
- `webhook_url` - Slack Webhook URL
- `message` - 消息内容

**可选参数:**
- `bot_name` - 机器人显示名称 (默认: "Tofi Bot")
- `icon` - 机器人图标 emoji (默认: ":robot_face:")

**示例:**
```yaml
nodes:
  notify:
    type: workflow
    config:
      action: "tofi/slack_notify"
    input:
      webhook_url: "${SLACK_WEBHOOK_URL}"
      message: "Workflow completed successfully!"
      bot_name: "Tofi Bot"
      icon: ":white_check_mark:"
```

---

### 5. `tofi/discord_notify` - Discord 通知

发送消息到 Discord 频道 (基于 Webhook)。

**必需参数:**
- `webhook_url` - Discord Webhook URL
- `message` - 消息内容

**可选参数:**
- `bot_name` - 机器人显示名称
- `avatar_url` - 机器人头像 URL

**示例:**
```yaml
nodes:
  notify:
    type: workflow
    config:
      action: "tofi/discord_notify"
    input:
      webhook_url: "${DISCORD_WEBHOOK_URL}"
      message: "Build #123 completed!"
      bot_name: "CI Bot"
```

---

### 6. `tofi/telegram_notify` - Telegram 通知

通过 Bot API 发送消息到 Telegram。支持两种方式:
- **用户自己的 Bot**: 在 workflow 中提供 `bot_token`
- **Tofi 官方 Bot**: 不提供 `bot_token`,使用环境变量 `TOFI_TELEGRAM_BOT_TOKEN`

**必需参数:**
- `chat_id` - 目标聊天 ID (总是必需)
- `message` - 消息内容

**可选参数:**
- `bot_token` - Telegram Bot Token (不提供则使用 Tofi 官方 Bot)
- `parse_mode` - 解析模式 ("Markdown" 或 "HTML")

**示例 1 - 使用用户自己的 Bot:**
```yaml
nodes:
  notify:
    type: workflow
    config:
      action: "tofi/telegram_notify"
    input:
      bot_token: "${TELEGRAM_BOT_TOKEN}"  # 用户自己的 Bot
      chat_id: "123456789"                 # 用户的 Chat ID
      message: "🚀 Deployment completed!"
      parse_mode: "Markdown"
```

**示例 2 - 使用 Tofi 官方 Bot:**
```yaml
nodes:
  notify:
    type: workflow
    config:
      action: "tofi/telegram_notify"
    input:
      # 不提供 bot_token,使用环境变量中的 TOFI_TELEGRAM_BOT_TOKEN
      chat_id: "123456789"  # 仅需提供用户的 Chat ID
      message: "✅ Task completed!"
```

---

### 7. `tofi/send_email` - 发送邮件

通过 SMTP 发送邮件 (支持 Gmail, Outlook, 自定义 SMTP)。

**必需参数:**
- `smtp_server` - SMTP 服务器地址
- `smtp_port` - SMTP 端口 (通常 587 或 465)
- `smtp_username` - SMTP 用户名
- `smtp_password` - SMTP 密码
- `from` - 发件人邮箱
- `to` - 收件人邮箱
- `subject` - 邮件主题
- `body` - 邮件正文

**示例:**
```yaml
nodes:
  send:
    type: workflow
    config:
      action: "tofi/send_email"
    input:
      smtp_server: "smtp.gmail.com"
      smtp_port: "587"
      smtp_username: "${SMTP_USERNAME}"
      smtp_password: "${SMTP_PASSWORD}"
      from: "bot@example.com"
      to: "user@example.com"
      subject: "Workflow Alert"
      body: "Your workflow has completed successfully."
```

---

### 8. `tofi/write_file` - 写入文件

写入内容到文件 (自动创建父目录)。

**必需参数:**
- `path` - 文件路径
- `content` - 文件内容

**示例:**
```yaml
nodes:
  save_report:
    type: workflow
    config:
      action: "tofi/write_file"
    input:
      path: "reports/result.txt"
      content: "{{previous_step.output}}"
```

---

### 9. `tofi/github_create_issue` - 创建 GitHub Issue

在 GitHub 仓库中创建 Issue。

**必需参数:**
- `github_token` - GitHub Personal Access Token
- `owner` - 仓库所有者
- `repo` - 仓库名称
- `title` - Issue 标题
- `body` - Issue 内容

**可选参数:**
- `labels` - 标签数组 (JSON 格式)

**示例:**
```yaml
nodes:
  create_issue:
    type: workflow
    config:
      action: "tofi/github_create_issue"
    input:
      github_token: "${GITHUB_TOKEN}"
      owner: "myorg"
      repo: "myrepo"
      title: "Bug found in production"
      body: "Error details: {{error_log.output}}"
      labels: '["bug", "priority-high"]'
```

---

## 使用说明

### 官方 Action 调用方式
```yaml
nodes:
  my_task:
    type: workflow
    config:
      action: "tofi/action_name"  # 使用 tofi/ 前缀
    input:
      param1: "value1"
      param2: "value2"
```

### 安全性
- ❌ **禁止直接访问** `action_library/` 目录
- ✅ **必须通过** `action: "tofi/xxx"` 调用
- ✅ **Secrets 管理**: 通过 `secret` 节点传递敏感信息

---

## 添加新 Action

1. 在 `action_library/` 创建新的 `.yaml` 文件
2. 使用 `{{inputs.xxx}}` 引用传入参数
3. 重新编译 Tofi (embed 会包含新文件)
4. 更新本 README

---

## 注意事项

- 所有 action 在编译时通过 `embed` 嵌入二进制
- 用户通过 `input` 传递参数,使用 `{{inputs.xxx}}` 引用
- Secrets 应由用户在自己的 workflow 中管理,不要硬编码在 action 中
