#!/usr/bin/env python3
"""Brave Video Search — find videos with metadata (duration, views, creator).

Usage:
    python3 videos.py "query" [--count N] [--freshness RANGE] [--search-lang LANG]

Options:
    --count N         Number of results (default: 10, max: 50)
    --freshness RANGE Time filter: pd (past day), pw (past week), pm (past month),
                      py (past year), or YYYY-MM-DDtoYYYY-MM-DD
    --search-lang LANG Content language code (default: en)

Environment:
    BRAVE_API_KEY — Brave Search API subscription token (required)
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def main():
    api_key = os.environ.get("BRAVE_API_KEY", "")
    if not api_key:
        print("Video search requires a Brave API key. Please configure it in Settings > Service Keys.")
        sys.exit(0)

    args = sys.argv[1:]
    if not args or not args[0].strip() or args[0].startswith("--"):
        print('Usage: python3 videos.py "query" [--count N] [--freshness RANGE] [--search-lang LANG]')
        sys.exit(1)

    query = args[0].strip()
    count = 10
    freshness = ""
    search_lang = "en"

    i = 1
    while i < len(args):
        if args[i] == "--count" and i + 1 < len(args):
            try:
                count = max(1, min(50, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        elif args[i] == "--freshness" and i + 1 < len(args):
            freshness = args[i + 1]
            i += 2
        elif args[i] == "--search-lang" and i + 1 < len(args):
            search_lang = args[i + 1]
            i += 2
        else:
            i += 1

    params = {
        "q": query,
        "count": str(count),
        "search_lang": search_lang,
        "text_decorations": "false",
    }
    if freshness:
        params["freshness"] = freshness

    url = f"https://api.search.brave.com/res/v1/videos/search?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"Brave Videos API error (HTTP {e.code}): {body}")
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    results = data.get("results", [])

    freshness_label = ""
    if freshness:
        freshness_label = {
            "pd": " (past 24 hours)",
            "pw": " (past week)",
            "pm": " (past month)",
            "py": " (past year)",
        }.get(freshness, f" ({freshness})")

    print(f'=== Videos: "{query}"{freshness_label} ===\n')

    if not results:
        print("No videos found.")
        return

    for idx, video in enumerate(results, 1):
        title = video.get("title", "")
        url_str = video.get("url", "")
        desc = video.get("description", "")
        age = video.get("age", "")
        meta = video.get("video", {})
        source = video.get("meta_url", {}).get("hostname", "")

        duration = meta.get("duration", "")
        views = meta.get("views")
        creator = meta.get("creator", "")
        publisher = meta.get("publisher", "")

        print(f"--- [{idx}] {title} ---")
        parts = []
        if creator:
            parts.append(f"Creator: {creator}")
        if publisher:
            parts.append(f"Platform: {publisher}")
        if duration:
            parts.append(f"Duration: {duration}")
        if views is not None:
            if views >= 1_000_000:
                parts.append(f"Views: {views / 1_000_000:.1f}M")
            elif views >= 1_000:
                parts.append(f"Views: {views / 1_000:.1f}K")
            else:
                parts.append(f"Views: {views}")
        if parts:
            print(" | ".join(parts))
        if source:
            print(f"Source: {source}")
        if age:
            print(f"Published: {age}")
        print(f"URL: {url_str}")
        if desc:
            print(f"Description: {desc}")
        print()

    print(f"[{len(results)} videos found]")


if __name__ == "__main__":
    main()
