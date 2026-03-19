---
name: app-manage
description: Edit, delete, run, rerun, and configure apps — schedule, notifications, and lifecycle
version: "5.0"
---

# App Manage

All operational actions on an existing App.

## Resolve App First

If user refers to an app by name, use **app-list** first to get the ID.

## Actions

### Edit Config

Use `tofi_update_app` with the fields to change. Supported fields:
- `name`, `description`, `prompt`, `model`, `skills`, `schedule`

Workflow:
1. Confirm what the user wants to change
2. Call `tofi_update_app` with only the changed fields
3. Show what was updated

### Delete

Use `tofi_delete_app`. **Always confirm first:**
- Show the app name and ask "Are you sure you want to delete 'xxx'?"
- Only proceed after explicit confirmation

### Run Now

Use `tofi_run_app` to trigger an immediate run.
- Tell the user: "App triggered — it's running in the background."
- The run creates a new session; user can check results later via **app-inspect**.

### Rerun

When user says "rerun that failed one" or "run it again":
- This is just `tofi_run_app` — it always runs the app's current prompt
- If the user wants to rerun with the **exact same context as a past run**, explain that rerun uses the latest prompt config. If the prompt has changed since then, the result may differ.

### Schedule Management

To change schedule:
- `tofi_update_app` with `schedule` field (JSON array format)
- Example: `[{"time":"09:00","repeat":{"type":"daily"}}]`

To activate/deactivate:
- `tofi_toggle_schedule` with `enabled: true/false`
- Deactivating cancels pending runs

Common schedule patterns to suggest:
- Daily: `[{"time":"09:00","repeat":{"type":"daily"}}]`
- Weekdays: `[{"time":"09:00","repeat":{"type":"weekly","days":["mon","tue","wed","thu","fri"]}}]`
- Weekly: `[{"time":"09:00","repeat":{"type":"weekly","days":["mon"]}}]`
- Hourly: `[{"time":"00:00","repeat":{"type":"hourly"}}]`

### Notification Targets

To view: `tofi_list_notify_targets` with `app_id`
To set: `tofi_set_notify_targets` with `app_id` and either `receiver_ids` or `all: true`

Workflow:
1. List available receivers: `tofi_list_notify_targets` (without app_id)
2. Confirm with user which receivers they want
3. Set targets: `tofi_set_notify_targets`

### Resend Last Result

When user says "send me that result again" or "push the last output":
1. Use **app-inspect** to get the latest successful run's session
2. Call `tofi_get_run_detail` to retrieve the output
3. Use `tofi_notify` to push it to the app's configured targets
