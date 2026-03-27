#!/usr/bin/env python3
"""Brave News Search — dedicated news search with freshness control.

Usage:
    python3 news.py "query" [--count N] [--freshness RANGE] [--search-lang LANG]
                             [--extra-snippets]

Options:
    --count N          Number of results (default: 5, max: 20)
    --freshness RANGE  Time filter: pd (past day), pw (past week), pm (past month),
                       py (past year), or YYYY-MM-DDtoYYYY-MM-DD (default: pw)
    --search-lang LANG Content language code (default: en)
    --extra-snippets   Include up to 5 additional excerpts per article

Environment:
    BRAVE_API_KEY — Brave Search API subscription token (required)
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def ddgs_news(query, count=10):
    """Fallback news search using DuckDuckGo."""
    try:
        from duckduckgo_search import DDGS
        with DDGS() as ddgs:
            results = list(ddgs.news(query, max_results=count))
            if not results:
                print(f"No news found for: {query}")
                return
            for i, r in enumerate(results, 1):
                print(f"\n--- News {i} ---")
                print(f"Title: {r.get('title', '')}")
                print(f"URL: {r.get('url', '')}")
                print(f"Source: {r.get('source', '')}")
                print(f"Date: {r.get('date', '')}")
                print(f"Snippet: {r.get('body', '')}")
    except ImportError:
        print("Error: duckduckgo-search package not installed.")
        print("Install with: pip install duckduckgo-search")
        sys.exit(1)


def main():
    api_key = os.environ.get("BRAVE_API_KEY", "")

    args = sys.argv[1:]
    if not args or not args[0].strip() or args[0].startswith("--"):
        print('Usage: python3 news.py "query" [--count N] [--freshness RANGE]')
        sys.exit(1)

    query = args[0].strip()
    count = 5
    freshness = "pw"  # past week by default
    search_lang = "en"
    extra_snippets = False

    i = 1
    while i < len(args):
        if args[i] == "--count" and i + 1 < len(args):
            try:
                count = max(1, min(20, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        elif args[i] == "--freshness" and i + 1 < len(args):
            freshness = args[i + 1]
            i += 2
        elif args[i] == "--search-lang" and i + 1 < len(args):
            search_lang = args[i + 1]
            i += 2
        elif args[i] == "--extra-snippets":
            extra_snippets = True
            i += 1
        else:
            i += 1

    # If no Brave API key, use DuckDuckGo fallback
    if not api_key:
        print("[No Brave API key — using DuckDuckGo fallback]")
        ddgs_news(query, count)
        sys.exit(0)

    # Use Brave News Search endpoint
    params = {
        "q": query,
        "count": str(count),
        "freshness": freshness,
        "search_lang": search_lang,
        "text_decorations": "false",
    }
    if extra_snippets:
        params["extra_snippets"] = "true"
    url = f"https://api.search.brave.com/res/v1/news/search?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"Brave News API error (HTTP {e.code}): {body}")
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    results = data.get("results", [])

    freshness_label = {
        "pd": "past 24 hours",
        "pw": "past week",
        "pm": "past month",
        "py": "past year",
    }.get(freshness, freshness)

    print(f'=== News: "{query}" ({freshness_label}) ===\n')

    if not results:
        print("No news articles found.")
        return

    for i, article in enumerate(results, 1):
        title = article.get("title", "")
        url_str = article.get("url", "")
        desc = article.get("description", "")
        source = article.get("meta_url", {}).get("hostname", "")
        age = article.get("age", "")
        extra = article.get("extra_snippets", [])

        print(f"--- [{i}] {title} ---")
        if source:
            print(f"Source: {source}")
        if age:
            print(f"Published: {age}")
        print(f"URL: {url_str}")
        if desc:
            print(f"Summary: {desc}")
        if extra:
            print("Additional content:")
            for snippet in extra:
                print(f"  {snippet}")
        print()

    print(f"[{len(results)} news articles found]")


if __name__ == "__main__":
    main()
