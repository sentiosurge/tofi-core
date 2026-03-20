---
name: app-list
description: List all Tofi Apps with status, schedule, and next run time
version: "5.0"
---

# List Apps

Call `tofi_list_apps` to retrieve all user apps.

## User-facing table format

| #  | ID              | Name           | Status   | Schedule      | Next Run         |
|----|-----------------|----------------|----------|---------------|------------------|
| 1  | daily-weather   | 每日天气播报    | ● active | daily 09:00   | 2026-03-20 09:00 |
| 2  | weekly-report   | Weekly Report  | ○ off    | weekly Mon 08 | —                |
| 3  | news-digest     | 新闻速递       | ● active | daily 07:30   | 2026-03-20 07:30 |

- ID is the kebab-case identifier (used in all operations)
- Name is the display name (free-form, any language)
- Briefly remind user they can create, run, edit, or delete apps

## No apps

No apps found. You can create one — just describe what you want!
