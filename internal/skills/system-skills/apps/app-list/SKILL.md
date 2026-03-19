---
name: app-list
description: List all Tofi Apps with status, schedule, and next run time
version: "4.3"
---

# List Apps

Call `tofi_list_apps` to retrieve all user apps.

## User-facing table format

```
| #  | Name            | Status   | Schedule      | Next Run         |
|----|-----------------|----------|---------------|------------------|
| 1  | daily-weather   | ● active | daily 09:00   | 2026-03-20 09:00 |
| 2  | weekly-report   | ○ off    | weekly Mon 08 | —                |
| 3  | news-digest     | ● active | daily 07:30   | 2026-03-20 07:30 |
```

- Show App ID mapping after table: `(IDs: 1=abc123, 2=def456, ...)`
- Briefly remind user they can create, run, edit, or delete apps

## No apps

```
No apps found. You can create one — just describe what you want!
```
