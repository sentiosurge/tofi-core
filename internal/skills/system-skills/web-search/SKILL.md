---
name: web-search
description: Search the web for real-time information using Brave Search or DuckDuckGo fallback
version: "3.1"
required_secrets: ["BRAVE_API_KEY"]
---

# Web Search Toolkit

> Best with Brave API key (Settings > Service Keys). Without key, DuckDuckGo fallback (reduced quality).
> **Note**: DuckDuckGo does NOT support `site:` operator — only Brave supports search operators.

## Tools

### `search.py` — Smart Search (Default)
```bash
python3 skills/web-search/scripts/search.py "query" [--count N] [--tokens N] [--freshness RANGE] [--search-lang LANG] [--result-filter TYPES]
```
- `--count N`: Sources (default: 5, max: 20) | `--tokens N`: Max tokens (default: 8192)
- `--freshness`: `pd`/`pw`/`pm`/`py` or `YYYY-MM-DDtoYYYY-MM-DD` (bypasses LLM Context when set)
- `--result-filter`: `discussions`, `faq`, `infobox`, `news`, `web`, `locations`
- **Do NOT use for news/time-sensitive queries** — use `news.py` instead.

### `news.py` — News Search
```bash
python3 skills/web-search/scripts/news.py "query" [--count N] [--freshness RANGE] [--extra-snippets]
```
- `--freshness` default: `pw` | `--extra-snippets`: Up to 5 additional excerpts per article
- **Use for breaking events, press coverage, anything time-sensitive.**

### `videos.py` — Video Search
```bash
python3 skills/web-search/scripts/videos.py "query" [--count N] [--freshness RANGE]
```

### `images.py` — Image Search
```bash
python3 skills/web-search/scripts/images.py "query" [--count N] [--safesearch MODE]
```
- `--safesearch`: `off`/`strict` (default: strict)

### `summarize.py` — AI Summary
```bash
python3 skills/web-search/scripts/summarize.py "query"
```
- Returns condensed AI summary with sources.

## Tips

- Decompose complex questions into multiple search calls.
- Use `site:domain.com` when user mentions a specific website (Brave only).
- For news/current events, **always** use `news.py`, not `search.py`.
- Cite sources inline: `[Site Name](URL)`.
