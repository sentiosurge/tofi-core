---
name: web-fetch
description: Fetch and read any web page using headless Chrome, extracting clean text content from URLs including JavaScript-rendered pages
version: "2.0"
---

# Web Fetch

Read any web page and get clean text content. Uses headless Chrome to render JavaScript, so it works with SPAs, dynamic pages, and static sites alike.

## Requirements

- Google Chrome or Chromium installed on the system
- Optional: `pip install trafilatura` for higher-quality text extraction

## Tool

```bash
python3 skills/web-fetch/scripts/fetch.py "URL" [--max-chars N]
```
- `--max-chars N`: Maximum characters to return (default: 8000, max: 50000)
- Renders JavaScript via headless Chrome before extracting text
- Uses trafilatura for intelligent article extraction (with regex fallback)

## When to Use

- Read a specific URL you already know (from search results, user-provided links, documentation pages)
- Get the full text of an article when a snippet is insufficient
- Fetch JavaScript-rendered pages (React, Vue, Angular SPAs)
- Read documentation, changelogs, release notes at a known URL
- Inspect web page content after client-side rendering
