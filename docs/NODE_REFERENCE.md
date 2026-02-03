# Tofi Node Reference

> Complete reference for all node types in Tofi workflow engine.
> Format designed for AI agents to generate valid workflow YAML.

---

## Node Schema

Every node follows this structure:

```yaml
<node_id>:                    # Unique identifier (required)
  type: "<node_type>"         # Node type (required)
  config:                     # Static configuration
    <key>: <value>
  input:                      # Dynamic inputs (supports {{}} references)
    - var:
        id: "<local_name>"
        from: "{{node_id.path}}"
  env:                        # Environment variables (shell nodes only)
    <KEY>: "<value>"
  next: ["<node_id>"]         # Nodes to execute on success
  on_failure: ["<node_id>"]   # Nodes to execute on failure
  dependencies: ["<node_id>"] # Wait for these nodes before starting (optional, auto-inferred)
  timeout: <seconds>          # Node-level timeout
  retry_count: <number>       # Retry attempts on failure
```

**Note on `next` and `dependencies`:**
- You only need to specify ONE of these fields; the engine auto-infers the other.
- If `A.next` contains `B`, then `B.dependencies` will automatically include `A`.
- If `B.dependencies` contains `A`, then `A.next` will automatically include `B`.
- This eliminates redundancy and prevents "orphan node" issues.

---

## Task Nodes

### shell

Execute shell commands with environment variable injection.

**Config:**
| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `script` | string | Yes | - | Shell script to execute |

**Env:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `<KEY>` | string | No | Custom environment variables |

**Node-level Options:**
| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `timeout` | number | 30 | Execution timeout in seconds |

**Magic Variables (auto-injected):**
| Variable | Description |
|----------|-------------|
| `TOFI_ARTIFACTS_DIR` | Path to write output files |
| `TOFI_EXECUTION_ID` | Current execution ID |

**Output:** stdout of the script (trimmed)

**Example:**
```yaml
build_project:
  type: "shell"
  env:
    NODE_ENV: "production"
    API_KEY: "{{secrets.api_key}}"
  config:
    script: |
      npm install
      npm run build
      echo "Build complete" > $TOFI_ARTIFACTS_DIR/build.log
  timeout: 300  # Override default 30s
  next: ["deploy"]
```

---

### ai

Call LLM APIs (OpenAI, Claude, Gemini) for text generation.

**Config:**
| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `model` | string | Yes | - | Model name or "openai-compatible" for custom endpoints |
| `api_key` | string | Yes* | - | API key (or use `use_system_key`) |
| `use_system_key` | boolean | No | false | Use system-configured API key |
| `endpoint` | string | Only if model="openai-compatible" | - | Full API endpoint URL |
| `system` | string | No | "" | System prompt |
| `prompt` | string | Yes | - | User prompt |
| `mcp_servers` | array | No | - | MCP server IDs for agent mode |

**Model auto-detection:**
- `claude*` → Anthropic API
- `gemini*` → Google Gemini API
- `gpt-*`, `o1-*`, `o3-*` → OpenAI API (Completions)
- `gpt-5*` → OpenAI API (Responses, new format)
- `openai-compatible` → User-provided endpoint (Ollama, vLLM, etc.)

**Output:** Generated text response

**Example (Standard):**
```yaml
summarize:
  type: "ai"
  config:
    model: "gpt-4o"
    api_key: "{{secrets.openai_key}}"
    system: "You are a helpful assistant."
    prompt: "Summarize this text: {{fetch_content}}"
  next: ["save_summary"]
```

**Example (Claude):**
```yaml
analyze:
  type: "ai"
  config:
    model: "claude-3-5-sonnet-20241022"
    api_key: "{{secrets.anthropic_key}}"
    prompt: "Analyze this data: {{data.input}}"
```

**Example (OpenAI Compatible - Ollama):**
```yaml
local_llm:
  type: "ai"
  config:
    model: "openai-compatible"
    endpoint: "http://localhost:11434/v1/chat/completions"
    prompt: "Explain quantum computing"
```

**Example (Agent with MCP):**
```yaml
research_agent:
  type: "ai"
  config:
    model: "gpt-4o"
    use_system_key: true
    mcp_servers: ["web_search", "calculator"]
    system: "You are a research assistant with tools."
    prompt: "Research the latest AI news"
```

---

### api

Make HTTP requests to external APIs.

**Config:**
| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | Yes | - | Request URL |
| `method` | string | No | "POST" | HTTP method (GET, POST, PUT, DELETE, etc.) |
| `headers` | object | No | {} | Request headers |
| `params` | object | No | {} | URL query parameters |
| `body` | string/object | No | - | Request body (auto-serialized if object) |

**Output:** Response body as string

**Example:**
```yaml
send_notification:
  type: "api"
  config:
    method: "POST"
    url: "https://api.slack.com/api/chat.postMessage"
    headers:
      Authorization: "Bearer {{secrets.slack_token}}"
      Content-Type: "application/json"
    body:
      channel: "#alerts"
      text: "Workflow completed: {{workflow_name}}"
```

---

### file

Select a file from the global file library for use in workflow.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `file_id` | string | Yes | User-defined file ID from file library |
| `accept` | string/array | No | Allowed file extensions (e.g., ".csv,.json") |

**Output:** Absolute path to the file

**Example:**
```yaml
load_dataset:
  type: "file"
  config:
    file_id: "sales_data_2024"
    accept: ".csv,.xlsx"
  next: ["process_data"]

process_data:
  type: "shell"
  config:
    script: "python analyze.py {{load_dataset}}"
```

---

### workflow

Call another workflow as a sub-process (handoff).

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `uses` | string | No* | Component reference (e.g., "tofi/ai_response@v2") |
| `file` | string | No* | Path to workflow YAML file |
| `workflow` | string | No* | Workflow ID (legacy) |
| `action` | string | No* | Action name (legacy) |
| `data` | object | No | Data payload to pass to sub-workflow |
| `secrets` | object | No | Secrets payload to pass to sub-workflow |

*One of `uses`, `file`, `workflow`, or `action` is required.

**Constraints:**
- Maximum recursion depth: **10** (prevents infinite loops)

**Output:** JSON object containing all sub-workflow node outputs

**Example (File Path):**
```yaml
call_processor:
  type: "workflow"
  config:
    file: "./processors/data_cleaner.yaml"
    data:
      raw_data: "{{fetch_data}}"
      output_format: "json"
```

**Example (Toolbox Component with Versioning):**
```yaml
send_telegram:
  type: "workflow"
  config:
    uses: "tofi/telegram_notify@v2"
    data:
      message: "{{summary}}"
    secrets:
      bot_token: "{{secrets.telegram_token}}"
```

**Example (Using Input Syntax):**
```yaml
call_with_inputs:
  type: "workflow"
  config:
    uses: "tofi/ai_response"
  input:
    - var:
        id: "prompt"
        from: "{{user_question}}"
    - secret:
        id: "api_key"
        from: "{{secrets.openai_key}}"
```

---

### hold

Pause workflow execution until manual approval via API.

**Config:** None required

**Input:**
| Field | Type | Description |
|-------|------|-------------|
| Any | Any | Context data passed to approver |

**Output:** Input data (pass-through on approve)

**Failure:** Returns error on reject

**Example:**
```yaml
approval_gate:
  type: "hold"
  input:
    - var:
        id: "deploy_target"
        from: "{{data.environment}}"
    - var:
        id: "changes_summary"
        from: "{{analyze.summary}}"
  next: ["deploy"]
  on_failure: ["notify_rejection"]
```

**Approval API:**
```bash
# Approve
POST /api/v1/executions/{exec_id}/nodes/approval_gate/approve
{"action": "approve"}

# Reject
POST /api/v1/executions/{exec_id}/nodes/approval_gate/approve
{"action": "reject"}
```

---

## Logic Nodes

### loop

Iterate over a list or range, executing a task for each item.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `mode` | string | Yes | "list" or "range" |
| `items` | array/string | If mode=list | Items to iterate (or JSON string) |
| `start` | number | If mode=range | Range start |
| `end` | number | If mode=range | Range end |
| `step` | number | No | Range step (default: 1) |
| `iterator` | string | No | Variable name for current item (default: "item") |
| `max_concurrency` | number | No | Max parallel iterations (default: 1) |
| `fail_fast` | boolean | No | Stop on first error (default: false) |
| `task` | object | Yes | Task definition to execute per item |

**Output:** JSON array of all iteration results

**Example (List):**
```yaml
process_users:
  type: "loop"
  config:
    mode: "list"
    items: ["alice", "bob", "charlie"]
    iterator: "username"
    max_concurrency: 3
    task:
      type: "api"
      config:
        url: "https://api.example.com/users/{{username}}"
        method: "GET"
```

**Example (Range):**
```yaml
batch_process:
  type: "loop"
  config:
    mode: "range"
    start: 1
    end: 10
    step: 1
    iterator: "page"
    task:
      type: "api"
      config:
        url: "https://api.example.com/data?page={{page}}"
```

**Example (Dynamic Items):**
```yaml
process_results:
  type: "loop"
  config:
    mode: "list"
    items: "{{fetch_list}}"  # JSON array from previous node
    iterator: "item"
    task:
      type: "shell"
      config:
        script: "echo Processing: {{item}}"
```

---

### compare

Compare two values and output `"true"` or `"false"`. Supports multiple data types.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `left` | string | Yes | Left operand |
| `right` | string | Yes | Right operand |
| `operator` | string | Yes | Comparison operator |

**Operators by Type:**

| Category | Operators | Type Requirements |
|----------|-----------|-------------------|
| **Universal** | `==`, `!=` | Try number first, fallback to string |
| **Numeric** | `>`, `<`, `>=`, `<=` | Both must be valid numbers |
| **Numeric** | `between` | left=number, right=`[min, max]` array |
| **String** | `contains`, `not_contains` | Converted to string |
| **String** | `starts_with`, `ends_with` | Converted to string |
| **String** | `matches` | left=string, right=regex pattern |
| **List** | `in`, `not_in` | right must be JSON array |

**Output:** `"true"` or `"false"` (string)

**Error:** Throws if type conversion fails (e.g., non-numeric for `>`)

**Example:**
```yaml
check_score:
  type: "compare"
  config:
    left: "{{metrics.score}}"
    operator: ">"
    right: "80"
  next: ["branch_node"]

check_range:
  type: "compare"
  config:
    left: "{{value}}"
    operator: "between"
    right: "[10, 100]"

check_contains:
  type: "compare"
  config:
    left: "{{response}}"
    operator: "contains"
    right: "success"

check_in_list:
  type: "compare"
  config:
    left: "{{status}}"
    operator: "in"
    right: '["active", "pending"]'
```

---

### check

Check a single value and output `"true"` or `"false"`.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `value` | string | Yes | Value to check |
| `operator` | string | Yes | Check type |

**Operators:**
| Operator | Description |
|----------|-------------|
| `is_empty` | Value is empty or whitespace only |
| `not_empty` | Value is not empty |
| `is_true` | Value equals "true"/"1" (case-insensitive) |
| `is_false` | Value equals "false"/"0" (case-insensitive) |
| `is_number` | Value is a valid number |
| `is_json` | Value is valid JSON |

**Output:** `"true"` or `"false"` (string)

**Example:**
```yaml
check_data_exists:
  type: "check"
  config:
    value: "{{optional_data}}"
    operator: "not_empty"
  next: ["process_data"]

check_is_valid_json:
  type: "check"
  config:
    value: "{{api_response}}"
    operator: "is_json"
```

---

### branch

Route workflow based on a boolean condition. Reads a `"true"` or `"false"` value and routes to `on_true` or `on_false` nodes.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `condition` | string | Yes | Value to evaluate (typically `{{compare_node}}`) |
| `on_true` | array | No* | Nodes to execute when condition is truthy |
| `on_false` | array | No* | Nodes to execute when condition is falsy |

*At least one of `on_true` or `on_false` must be defined.

**Truthy values:** `"true"`, `"1"`, `"yes"`, any non-empty string
**Falsy values:** `"false"`, `"0"`, `"no"`, empty string

**Output:** `"true"` or `"false"` (passes through the evaluated boolean)

**Example (Standalone):**
```yaml
score_check:
  type: "compare"
  config:
    left: "{{score}}"
    operator: ">"
    right: "80"
  next: ["router"]

router:
  type: "branch"
  config:
    condition: "{{score_check}}"
    on_true: ["high_score_handler"]
    on_false: ["low_score_handler"]
```

**Example (Combined with Compare):**

In practice, `compare` + `branch` are often used together. The frontend UI combines them into a single "Compare" node that auto-generates both:

```yaml
# Frontend shows this as ONE node, but generates TWO:
score_check:
  type: "compare"
  config:
    left: "{{score}}"
    operator: ">"
    right: "80"
  next: ["score_check_branch"]

score_check_branch:
  type: "branch"
  config:
    condition: "{{score_check}}"
    on_true: ["high_score_handler"]
    on_false: ["low_score_handler"]
```

---

## Data Nodes

### var

Define static or computed values.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `value` | string/number | Yes | Single value to store |

**Output:** The stored value as a string

**Example:**
```yaml
api_endpoint:
  type: "var"
  config:
    value: "https://api.example.com/v1"

max_retries:
  type: "var"
  config:
    value: 3
```

**Usage:**
```yaml
call_api:
  type: "api"
  config:
    url: "{{api_endpoint}}"
```

---

### secret

Define sensitive values with automatic log masking.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `<key>` | string | Yes | Secret key-value pairs |

**Value Formats:**
| Format | Description |
|--------|-------------|
| `"literal_value"` | Direct string value |
| `"env.VAR_NAME"` | Read from environment variable |
| `"{{env.VAR_NAME}}"` | Read from environment variable (template syntax) |

**Output:** JSON object with secret values (masked as `********` in logs)

**Example:**
```yaml
api_secrets:
  type: "secret"
  config:
    openai_key: "env.OPENAI_API_KEY"     # From env var
    db_password: "{{env.DB_PASSWORD}}"   # Template syntax
    static_token: "sk-xxx-literal"       # Direct value

call_ai:
  type: "ai"
  config:
    api_key: "{{api_secrets.openai_key}}"
    # Log shows: api_key: ********
```

---

### dict

Extract fields from JSON and build structured objects.

**Config:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `input` | string | No | Source JSON string |
| `fields` | array | Yes | Field definitions |

**Field Definition:**
| Field | Type | Description |
|-------|------|-------------|
| `key` | string | Output field name |
| `value` | string | Value expression |

**Value Expressions:**
- `"input.path.to.field"` - Extract from input JSON
- `"{{node_id}}"` - Reference another node
- `"literal"` - Literal string

**Output:** JSON object with extracted fields

**Example:**
```yaml
parse_response:
  type: "dict"
  config:
    input: "{{api_response}}"
    fields:
      - key: "user_id"
        value: "input.data.user.id"
      - key: "email"
        value: "input.data.user.email"
      - key: "status"
        value: "active"
      - key: "timestamp"
        value: "{{current_time}}"
```

---

## Base Nodes

### virtual

Logical grouping or synchronization point with no execution logic.

**Config:** None

**Output:** "VIRTUAL_OK"

**Use Cases:**
- Wait for multiple parallel branches to complete (fan-in)
- Logical workflow organization
- Placeholder for future expansion

**Example:**
```yaml
# Parallel branches
task_a:
  type: "shell"
  config:
    script: "echo A"
  next: ["sync_point"]

task_b:
  type: "shell"
  config:
    script: "echo B"
  next: ["sync_point"]

# Synchronization (waits for both task_a and task_b)
sync_point:
  type: "virtual"
  dependencies: ["task_a", "task_b"]
  next: ["final_step"]
```

---

## Complete Workflow Example

```yaml
id: data_pipeline
name: "Data Processing Pipeline"
description: "Fetch, process, and report on data"
timeout: 600

data:
  source_url: "https://api.example.com/data"

secrets:
  api_key: "{{env.API_KEY}}"
  slack_webhook: "{{env.SLACK_WEBHOOK}}"

nodes:
  # 1. Fetch data from API
  fetch_data:
    type: "api"
    config:
      method: "GET"
      url: "{{data.source_url}}"
      headers:
        Authorization: "Bearer {{secrets.api_key}}"
    timeout: 30
    next: ["validate_data"]
    on_failure: ["notify_error"]

  # 2. Validate data structure
  validate_data:
    type: "check"
    config:
      mode: "exists"
      value: "{{fetch_data}}"
    next: ["process_items"]
    on_failure: ["notify_error"]

  # 3. Process each item
  process_items:
    type: "loop"
    config:
      mode: "list"
      items: "{{fetch_data}}"
      iterator: "item"
      max_concurrency: 5
      task:
        type: "ai"
        config:
          model: "gpt-4o-mini"
          use_system_key: true
          prompt: "Summarize: {{item}}"
    next: ["generate_report"]

  # 4. Generate final report
  generate_report:
    type: "shell"
    config:
      script: |
        echo "# Report" > $TOFI_ARTIFACTS_DIR/report.md
        echo "Processed items: {{process_items}}" >> $TOFI_ARTIFACTS_DIR/report.md
    next: ["approval_gate"]

  # 5. Wait for approval
  approval_gate:
    type: "hold"
    input:
      - var:
          id: "report_preview"
          from: "{{generate_report}}"
    next: ["notify_success"]
    on_failure: ["notify_rejection"]

  # 6. Success notification
  notify_success:
    type: "api"
    config:
      method: "POST"
      url: "{{secrets.slack_webhook}}"
      body:
        text: "Pipeline completed successfully!"

  # Error handlers
  notify_error:
    type: "api"
    config:
      method: "POST"
      url: "{{secrets.slack_webhook}}"
      body:
        text: "Pipeline failed!"

  notify_rejection:
    type: "api"
    config:
      method: "POST"
      url: "{{secrets.slack_webhook}}"
      body:
        text: "Pipeline rejected by approver"
```

---

## Global Defaults

### System Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| Server port | 8080 | HTTP API port |
| Max workers | 10 | Concurrent workflow limit |
| Worker queue | 100 | Pending workflow buffer |
| Shell timeout | 30s | Default shell execution timeout |
| Handoff max depth | 10 | Maximum workflow recursion |

### Node Defaults

| Node | Parameter | Default |
|------|-----------|---------|
| `shell` | timeout | 30 seconds |
| `loop` | iterator | "item" |
| `loop` | max_concurrency | 1 |
| `loop` | step | 1 |
| `loop` | fail_fast | false |
| `api` | method | "POST" |
| `ai` | provider | Auto-detected from model |
| `ai` | use_system_key | false |

---

## Quick Reference Table

| Type | Category | Purpose | Key Config |
|------|----------|---------|------------|
| `shell` | Task | Execute shell commands | `script` |
| `ai` | Task | LLM text generation | `model`, `prompt` |
| `api` | Task | HTTP requests | `url`, `method` |
| `file` | Task | Load file from library | `file_id` |
| `workflow` | Task | Call sub-workflow | `uses` or `file` |
| `hold` | Task | Wait for approval | (input data) |
| `loop` | Logic | Iterate items | `mode`, `items`, `task` |
| `compare` | Logic | Compare two values → true/false | `left`, `operator`, `right` |
| `check` | Logic | Check single value → true/false | `value`, `operator` |
| `branch` | Logic | Route based on boolean | `condition`, `on_true`, `on_false` |
| `var` | Data | Define values | `value` |
| `secret` | Data | Sensitive values | key-values (supports env) |
| `dict` | Data | JSON extraction | `input`, `fields` |
| `virtual` | Base | Sync point | (none) |
