---
name: app-inspect
description: View app details, run history, and specific run output
version: "5.0"
---

# App Inspect

Deep-dive into a specific App. Covers three levels of detail:

## Level 1: App Status

Show full metadata for one app. Gather from multiple tools:

1. Use **app-list** (or `tofi_list_apps`) to find the app and get metadata
2. `tofi_list_app_runs` with `limit: 5` for recent runs
3. `tofi_list_notify_targets` with `app_id` for notification config

### User-facing format

```
╭─ daily-weather ──────────────────────────────────────────╮
│                                                          │
│  Status:    ● active          Model: gpt-5-mini          │
│  Schedule:  daily at 09:00    Next:  2026-03-20 09:00    │
│  ID:        abc12345-6789                                │
│  Notify:    Jack (Telegram)                              │
│                                                          │
│  Recent runs:                                            │
│  | # | Status   | Time        | Trigger   | Duration |  │
│  |---|----------|-------------|-----------|----------|  │
│  | 1 | ✓ done   | 03-19 09:01 | scheduled | 12s      |  │
│  | 2 | ✓ done   | 03-18 09:00 | scheduled | 8s       |  │
│  | 3 | ✗ failed | 03-17 09:01 | manual    | 3s       |  │
│                                                          │
╰──────────────────────────────────────────────────────────╯
```

After showing status, remind user they can:
- Say a **run number** to see its full output
- Use **app-manage** to edit, delete, run, or change config

## Level 2: Run History

When user wants to see more runs or specifically asks about run history:

Call `tofi_list_app_runs` with higher limit (up to 20).

```
| #  | Status   | Time        | Trigger   | Duration | Session         |
|----|----------|-------------|-----------|----------|-----------------|
| 1  | ✓ done   | 03-19 09:01 | scheduled | 12s      | s_a1b2c3d4      |
| 2  | ✓ done   | 03-18 09:00 | scheduled | 8s       | s_f7g8h9i0      |
| 3  | ✗ failed | 03-17 09:01 | manual    | 3s       | s_m3n4o5p6      |
```

If a run failed, proactively offer: "Run #3 failed — want me to show the full output?"

## Level 3: Specific Run Detail

When user asks to see a specific run's output (e.g., "show me run #3" or "what happened in that failed run"):

Call `tofi_get_run_detail` with the run's session_id. This returns:
- All messages (user prompts, AI responses, tool calls and results)
- Thinking/reasoning content
- Error messages if the run failed

Present the output clearly:
- Show each step in order (prompt → thinking → tool calls → response)
- Highlight errors in red/bold if it was a failed run
- For successful runs, focus on the final output

After showing detail, offer:
- "Want me to **rerun** this app?" → use **app-manage**
- "Want me to **resend** the result to your notification targets?" → use `tofi_notify`
