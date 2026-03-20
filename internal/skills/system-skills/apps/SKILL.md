---
name: apps
description: Tofi App management — create, inspect, manage, and run automated AI tasks
version: "5.0"
---

# Apps

Manage Tofi Apps — automated AI tasks that can run on schedules or on demand.

## Sub-skills

| Skill | When to use |
|-------|-------------|
| **app-list** | User wants to see all their apps |
| **app-inspect** | User wants details on a specific app: status, schedule, run history, run output |
| **app-create** | User wants to create a new app |
| **app-manage** | User wants to edit, delete, run, rerun, or change schedule/notifications for an app |

Route to the appropriate sub-skill based on the user's intent. If unclear, use **app-list** first to orient.

## Available Tools

| Tool | Purpose |
|------|---------|
| `tofi_list_apps` | List all apps |
| `tofi_create_app` | Create app |
| `tofi_update_app` | Update app config |
| `tofi_delete_app` | Delete app |
| `tofi_run_app` | Trigger a manual run |
| `tofi_list_app_runs` | List run history |
| `tofi_get_run_detail` | Get full output of a specific run (messages, tool calls) |
| `tofi_toggle_schedule` | Enable/disable schedule |
| `tofi_list_notify_targets` | List notification receivers |
| `tofi_set_notify_targets` | Set who gets notified on completion |
| `tofi_list_models` | List available AI models (for selecting app model) |
| `tofi_display_app_plan` | Display app plan in rich TUI format (use for create/edit confirmation) |

## General Rules

- Always confirm before destructive actions (delete, deactivate)
- Use **app-list** (or `tofi_list_apps`) first when the user references an app by name — to resolve the ID
- Respond in the user's language

## Display Format (applies to ALL sub-skills)

Two output modes — choose based on context:

### 1. User-facing (user explicitly asked to see something)

Use **tables** and structured formatting:
- Markdown tables with aligned columns
- Status icons: `●` active, `○` inactive, `✓` done, `✗` failed, `⏳` running
- Dates in human-friendly format: "03-18 09:00" (not raw ISO)
- IDs shown compactly: first 8 chars or numbered references `#1, #2, ...`
- Use the user's language for headers and labels

### 2. Intermediate (called as a step within another operation)

Return **concise, data-only** text:
- No tables, no formatting, no icons
- Just the facts needed for the next step
- Example: "Found 'daily-weather' (ID: abc123), active, schedule: daily 09:00"
