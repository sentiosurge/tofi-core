---
name: app-create
description: Create a new Tofi App from natural language — decompose user intent into app config
version: "8.0"
---

# Create App

When the user describes an automation they want, decompose it into a complete app definition.

## Step 1: Understand

From the user's description, determine:
1. **What** — core purpose (what should the AI do each run?)
2. **When** — schedule or manual-only?
3. **Who** — notification targets? (optional — the platform auto-delivers to configured receivers)

**Do NOT ask clarifying questions upfront.** If the user gave enough context, go straight to Step 2 and produce a complete plan. Present it for confirmation — the user can adjust from there. Only ask if the request is genuinely too vague to form any reasonable plan (e.g., "make me an app").

## Step 2: Decompose

- **id**: kebab-case (e.g., `daily-weather-report`) — unique, immutable identifier
- **name**: display name, any language (e.g., "Daily Weather Report") — optional, defaults to id
- **description**: one-line summary (< 80 chars)
- **prompt**: ⚠️ THIS IS THE MOST IMPORTANT FIELD. You must craft a rich, complete prompt — never just echo the user's words. Even if the user gives one sentence, you must expand it into a full AI instruction. Include ALL of the following:
  1. **Identity / Soul** — who is this AI? Give it a persona, role, expertise area (e.g., "You are a veteran Wall Street analyst with 20 years of experience, known for your contrarian insights and Buffett-style value investing philosophy")
  2. **Task** — what exactly to do each run, step by step
  3. **Tone & Style** — writing style, formality level, personality traits (e.g., "witty but data-driven", "concise and actionable", "explain like talking to a friend")
  4. **Output Format** — structure of the output (sections, bullet points, tables, emojis, length limits)
  5. **Constraints** — what NOT to do, edge cases, error handling (e.g., "if market is closed, say so and skip analysis")
  6. **Language** — output language (infer from user's language)

  ⚠️ **NEVER mention notifications/Telegram/Slack/Email in the prompt.** The prompt is ONLY for the AI's task logic — what data to fetch, how to analyze, what to output. Notification delivery is handled automatically by the platform runtime after the App finishes. The AI's output IS the deliverable — just produce good content.

  The prompt should be 200-500 words. Think of it as writing a "soul" for this AI agent. A lazy one-liner prompt is UNACCEPTABLE.

- **model**: call `tofi_list_models` to get available models, then pick one matching the task complexity
- **skills**:
  1. First check user's installed skills (they are already loaded in context)
  2. If no matching skill exists, suggest the user install one or describe what capability is needed
- **schedule** (if timed):
  1. Call `tofi_get_time` (no args) to get the user's local timezone — use it as default, do NOT ask the user for timezone
  2. **Infer the best time from context.** If the user says "每天早上", pick 08:00. If "每天晚上", pick 20:00. If "工作日", pick weekdays. If the user doesn't specify a time at all, pick a sensible default (e.g., 09:00 for daily reports, 08:00 for morning tasks). Never ask the user to confirm the time separately — just include it in the Step 3 summary.
  3. Format: `{"entries":[{"time":"09:00","repeat":{"type":"daily"},"enabled":true}],"timezone":"America/Los_Angeles"}`
  4. Repeat types: `daily`, `weekly` (with `days`:["mon","tue",...]), `monthly` (with `day_of_month`)

## Step 2.5: Notification targets (if user wants push)

If the user mentioned sending results somewhere (Telegram/Slack/Email/push/通知/发送):
1. Call `tofi_list_notify_targets` (without `app_id`) to list all available receivers
2. If only one receiver exists, auto-select it — don't ask
3. If multiple receivers, ask which ones
4. Record the selected `receiver_ids` for Step 4

If user didn't mention notifications, skip this step. The platform will still auto-deliver to any globally configured receivers.

## Step 3: Show Plan → Confirm → Execute (ONE PASS)

**You MUST call `tofi_display_app_plan` before creating the app. NEVER skip this step.**

Call `tofi_display_app_plan` with all fields:
- `id`, `name` (if different from id), `description`, `prompt` (full text), `model`
- `schedule` — human-readable (e.g., "Daily 08:00", "Weekdays 09:00")
- `timezone` — from Step 2
- `skills`, `notify` — if applicable

The TUI will render a formatted box. Then ask the user to confirm or adjust.

### Recognizing Confirmation

The user may confirm in many ways. ALL of the following count as explicit confirmation — **do NOT ask again**:
- Direct: "确认", "创建", "好", "OK", "yes", "对", "行", "可以", "没问题"
- Enthusiastic: "冲", "搞", "干", "上", "走", "开搞", "just do it"
- Dialect: "中" (河南话=确认), "得" (四川话=好), "成" (=OK)
- Implied: "好的", "就这样", "不用改了", "直接创建"

**When the user confirms, IMMEDIATELY execute Step 4. Do NOT show the plan again. Do NOT ask for re-confirmation. ONE confirmation is enough.**

If the user wants changes, apply them and show the updated plan once more.

## Step 4: Execute

Only after user confirms the plan:

1. `tofi_create_app` with all fields (schedule includes timezone)
2. If schedule provided → `tofi_toggle_schedule` with `enabled: true`
3. If user wants notifications → `tofi_set_notify_targets` with the `receiver_ids` from Step 2.5
4. Offer: "Want me to run it once to test?"
   - If yes → use **app-manage** (run now)
   - After test → use **app-inspect** to show the result
