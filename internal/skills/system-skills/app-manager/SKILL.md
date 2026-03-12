---
name: app-manager
description: Create, update, delete, and manage Tofi Apps via API
version: "1.0"
required_secrets: ["TOFI_TOKEN"]
---

# App Manager Toolkit

Manage Tofi Apps directly via API scripts. **Always use these scripts** to create, update, or delete apps — never propose changes that require frontend execution.

Environment variables `TOFI_API_URL` and `TOFI_TOKEN` are automatically injected.

## Tools

### `manage.py list` — List all apps
```bash
python3 skills/app-manager/scripts/manage.py list
```
Returns JSON array of all apps with id, name, description, is_active, schedule, model.

### `manage.py get <app_id>` — Get app details
```bash
python3 skills/app-manager/scripts/manage.py get <app_id>
```
Returns full JSON of a single app.

### `manage.py create` — Create a new app
```bash
python3 skills/app-manager/scripts/manage.py create --name "App Name" --prompt "What the app does..." [options]
```
Options:
- `--name NAME` (required): App display name
- `--prompt PROMPT` (required): What the app should do each run
- `--description DESC`: Brief description
- `--model MODEL`: Model ID (e.g., claude-sonnet-4-20250514, gpt-4o)
- `--skills SKILL1,SKILL2`: Comma-separated skill IDs
- `--schedule JSON`: Schedule rules as JSON string (e.g., `'{"entries":[{"time":"09:00","repeat":{"type":"daily"},"enabled":true}],"timezone":"Asia/Shanghai"}'`)
- `--system-prompt TEXT`: Custom system prompt
- `--capabilities JSON`: Capabilities config as JSON (e.g., `'{"web_search":{"enabled":true}}'`)

Returns the created app as JSON.

### `manage.py update <app_id>` — Update an existing app
```bash
python3 skills/app-manager/scripts/manage.py update <app_id> [options]
```
Same options as `create`. Only specified fields are updated.

### `manage.py delete <app_id>` — Delete an app
```bash
python3 skills/app-manager/scripts/manage.py delete <app_id>
```

### `manage.py activate <app_id>` — Enable scheduling
```bash
python3 skills/app-manager/scripts/manage.py activate <app_id>
```

### `manage.py deactivate <app_id>` — Disable scheduling
```bash
python3 skills/app-manager/scripts/manage.py deactivate <app_id>
```

### `manage.py run <app_id>` — Run app immediately
```bash
python3 skills/app-manager/scripts/manage.py run <app_id>
```

## Workflow

1. **Understand** the user's request — ask clarifying questions if needed
2. **Research** if needed (web search, skill search)
3. **Describe** your plan to the user in text, listing what you will create/change
4. **Wait for confirmation** — only proceed after the user says yes / approves
5. **Execute** using the scripts above
6. **Verify** by running `list` or `get` to confirm the changes

**Important**: Always describe what you plan to do BEFORE executing. For destructive actions (delete, deactivate), double-check with the user.
