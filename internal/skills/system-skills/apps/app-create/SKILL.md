---
name: app-create
description: Create a new Tofi App from natural language — decompose user intent into app config
version: "5.0"
---

# Create App

When the user describes an automation they want, decompose it into a complete app definition.

## Step 1: Understand

From the user's description, determine:
1. **What** — core purpose (what should the AI do each run?)
2. **When** — schedule or manual-only?
3. **Who** — notification targets? (optional)

**Do NOT ask clarifying questions upfront.** If the user gave enough context, go straight to Step 2 and produce a complete plan. Present it for confirmation — the user can adjust from there. Only ask if the request is genuinely too vague to form any reasonable plan (e.g., "make me an app").

## Step 2: Decompose

- **id**: kebab-case (e.g., `daily-weather-report`) — unique, immutable identifier
- **name**: display name, any language (e.g., "Daily Weather Report") — optional, defaults to id
- **description**: one-line summary (< 80 chars)
- **prompt**: clear, actionable instruction for the AI — include steps, output format, tone, error handling
- **model**: call `tofi_list_models` to get available models, then pick one matching the task complexity
- **skills**:
  1. First check user's installed skills (they are already loaded in context)
  2. If no matching skill exists, suggest the user install one or describe what capability is needed
- **schedule** (if timed): JSON array format
  - Daily: `[{"time":"09:00","repeat":{"type":"daily"}}]`
  - Weekdays: `[{"time":"09:00","repeat":{"type":"weekly","days":["mon","tue","wed","thu","fri"]}}]`
  - Weekly: `[{"time":"09:00","repeat":{"type":"weekly","days":["mon"]}}]`

## Step 2.5: Notification targets (if user wants push)

If the user mentioned notifications/push/提醒:
1. Call `tofi_list_notify_targets` (without `app_id`) to list all available receivers
2. Ask user which receivers to notify, or confirm "all"
3. Record the selected `receiver_ids` for Step 4

If user didn't mention notifications, skip this step.

## Step 3: Confirm

Present a summary in the user's language before creating:
```
名称: daily-weather-report
描述: 每天早上查询天气并推送
Prompt: [前 50 字...]
模型: gpt-4o-mini
调度: 每天 08:00
通知: Jack (Telegram)
```

Wait for user confirmation.

## Step 4: Execute

1. `tofi_create_app` with all fields
2. If schedule provided → `tofi_toggle_schedule` with `enabled: true`
3. If user wants notifications → `tofi_set_notify_targets` with the `receiver_ids` from Step 2.5
4. Offer: "Want me to run it once to test?"
   - If yes → use **app-manage** (run now)
   - After test → use **app-inspect** to show the result
