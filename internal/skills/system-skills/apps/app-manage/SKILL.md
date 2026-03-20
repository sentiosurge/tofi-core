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

Infer what needs to change from the user's message. ID is immutable; all other fields (`name`, `description`, `prompt`, `model`, `skills`, `schedule`) can be changed.

- **Single-field change** (e.g., "change model to gpt-4o"): call `tofi_update_app` directly, show what was updated.
- **Multi-field change** (e.g., "overhaul the prompt and schedule"): call `tofi_display_app_plan` first to show the proposed changes, wait for confirmation, then execute `tofi_update_app`.

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
- `tofi_update_app` with `schedule` field (JSON object with entries + timezone)
- Call `tofi_get_time` to get user's local timezone if not already known
- Example: `{"entries":[{"time":"09:00","repeat":{"type":"daily"},"enabled":true}],"timezone":"America/Los_Angeles"}`

To activate/deactivate:
- `tofi_toggle_schedule` with `enabled: true/false`
- Deactivating cancels pending runs

Common repeat types: `daily`, `weekly` (with `days`), `monthly` (with `day_of_month`)

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
