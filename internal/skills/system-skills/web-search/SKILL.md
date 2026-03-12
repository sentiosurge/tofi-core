---
name: web-search
description: Search the web for real-time information using Brave Search API
version: "3.0"
required_secrets: ["BRAVE_API_KEY"]
---

# Web Search Toolkit

You have 5 search tools. Use them **together** — most good answers need 2-4 tool calls.

## Tools

### `search.py` — Smart Search (Default)
```bash
python3 skills/web-search/scripts/search.py "query" [--count N] [--tokens N] [--freshness RANGE] [--search-lang LANG] [--result-filter TYPES]
```
- `--count N`: Sources (default: 5, max: 20) | `--tokens N`: Max tokens (default: 8192, max: 32768)
- `--freshness RANGE`: `pd`/`pw`/`pm`/`py` or `YYYY-MM-DDtoYYYY-MM-DD` (when set, bypasses LLM Context and uses web search)
- `--search-lang LANG`: Content language (default: en) | `--result-filter TYPES`: `discussions`, `faq`, `infobox`, `news`, `web`, `locations`
- Returns actual page content. **Your default first choice for general queries.** Do NOT use for news or time-sensitive queries — use `news.py` instead.

### `news.py` — News Search
```bash
python3 skills/web-search/scripts/news.py "query" [--count N] [--freshness RANGE] [--search-lang LANG] [--extra-snippets]
```
- `--freshness RANGE`: `pd`/`pw`/`pm`/`py` or `YYYY-MM-DDtoYYYY-MM-DD` (default: pw)
- `--search-lang LANG`: Content language (default: en) | `--extra-snippets`: Up to 5 additional excerpts per article
- Returns news articles with dates and publishers. Use when the information need is inherently time-sensitive.

### `videos.py` — Video Search
```bash
python3 skills/web-search/scripts/videos.py "query" [--count N] [--freshness RANGE] [--search-lang LANG]
```
- `--count N`: Results (default: 10, max: 50) | `--freshness RANGE`: Same as above
- Returns videos with title, URL, creator, duration, view count, publish date. Use when the user needs video content.

### `images.py` — Image Search
```bash
python3 skills/web-search/scripts/images.py "query" [--count N] [--search-lang LANG] [--safesearch MODE]
```
- `--count N`: Results (default: 10, max: 200) | `--safesearch MODE`: `off`/`strict` (default: strict)
- Returns images with source, dimensions, and URLs. Use when the user needs visual content.

### `summarize.py` — AI Summary
```bash
python3 skills/web-search/scripts/summarize.py "query"
```
- Returns a condensed AI summary with sources. Use for quick overviews.

## Search Operators

Use these directly in search queries for `search.py`, `news.py`, `videos.py`:

| Operator | Effect |
|----------|--------|
| `site:domain.com` | Restrict to a specific website |
| `intitle:word` | Title must contain word |
| `inbody:word` | Page body must contain word |
| `filetype:pdf` | Match file type |
| `"exact phrase"` | Match exact phrase |
| `-term` | Exclude term |
| `lang:xx` | Filter by content language |
| `loc:XX` | Filter by country code |
| `A AND B` | Both terms required |
| `A OR B` | Either term |
| `NOT A` | Exclude term |

## How to Search

**ALWAYS search.** Do not answer from memory unless you are 100% certain. Do not guess.

### 1. Decompose the question

Identify all **distinct information needs**. Each one gets its own search call. Do not try to cover everything in a single search.

### 2. Choose tools by information type

Pick the tool that matches the **nature** of each information need. Combine tools freely:
- General knowledge, analysis, documentation → `search.py`
- **News, breaking events, press coverage, anything time-sensitive** → **`news.py`** (NEVER use `search.py` for news — it cannot guarantee freshness)
- Tutorials, demos, reviews in video form → `videos.py`
- Visual references, photos, diagrams → `images.py`
- Quick factual overview → `summarize.py`

> ⚠️ **Critical**: If the task involves news, current events, stock market, or any "latest/recent" information, you **MUST** use `news.py`, not `search.py`. The `search.py` tool uses LLM Context which may return stale cached content.

### 3. Use `site:` when the user mentions a specific website

When the user mentions or implies a specific website (TMDB, Reddit, GitHub, Wikipedia, YouTube, Stack Overflow, etc.), **always** include `site:domain.com` in the query string. This applies to `search.py`, `news.py`, and `videos.py`.

### 4. Iterate until sufficient

After each tool call, ask: do I have enough to write a thorough response covering **all** aspects? If not, make another call — a different query, a different tool, or a `site:` search. Do not stop at one call unless the question is trivially simple.

### 5. Adjust parameters by complexity

- Simple factual question: `--count 3 --tokens 4096`
- Multi-faceted research: `--count 10 --tokens 16384`
- Recent events: add `--freshness pd` or `--freshness pw`
- Only use optional parameters when you have a clear reason. Defaults work well for most queries.

## Response Format

Write **flowing paragraphs**, not bullet-point lists. Synthesize multiple sources into coherent prose.

**Citations**: Use inline `[Site Name](URL)` after each claim. Weave citations into the text — do not list them at the end.

**Structure**: Use headings (`##`) to organize aspects. Include specific data from sources. End with a brief analysis in your own words. Respond in the user's language.
