#!/usr/bin/env python3
"""Brave LLM Context search — returns actual web page content chunks for AI.

Usage:
    python3 search.py "query" [--count N] [--tokens N] [--freshness RANGE]
                               [--search-lang LANG] [--result-filter TYPES]

Options:
    --count N             Max number of URLs to fetch content from (default: 5, max: 20)
    --tokens N            Max total tokens in response (default: 8192, max: 32768)
    --freshness RANGE     Time filter: pd/pw/pm/py or YYYY-MM-DDtoYYYY-MM-DD (bypasses LLM Context, uses web search)
    --search-lang LANG    Content language code (default: en)
    --result-filter TYPES Result types for fallback: discussions, faq, infobox, news, web, locations

Environment:
    BRAVE_API_KEY — Brave Search API subscription token (required)
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def ddgs_search(query, count=5, region="wt-wt"):
    """Fallback search using DuckDuckGo (no API key needed)."""
    try:
        from duckduckgo_search import DDGS
        with DDGS() as ddgs:
            results = list(ddgs.text(query, max_results=count, region=region))
            if not results:
                print(f"No results found for: {query}")
                return
            for i, r in enumerate(results, 1):
                print(f"\n--- Result {i} ---")
                print(f"Title: {r.get('title', '')}")
                print(f"URL: {r.get('href', '')}")
                print(f"Snippet: {r.get('body', '')}")
    except ImportError:
        print("Error: duckduckgo-search package not installed.")
        print("Install with: pip install duckduckgo-search")
        sys.exit(1)


def main():
    api_key = os.environ.get("BRAVE_API_KEY", "")

    # Parse arguments
    args = sys.argv[1:]
    if not args or not args[0].strip() or args[0].startswith("--"):
        print('Usage: python3 search.py "query" [--count N] [--tokens N]')
        sys.exit(1)

    query = args[0].strip()
    count = 5
    max_tokens = 8192
    freshness = ""
    search_lang = "en"
    result_filter = ""

    i = 1
    while i < len(args):
        if args[i] == "--count" and i + 1 < len(args):
            try:
                count = max(1, min(20, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        elif args[i] == "--tokens" and i + 1 < len(args):
            try:
                max_tokens = max(1024, min(32768, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        elif args[i] == "--freshness" and i + 1 < len(args):
            freshness = args[i + 1]
            i += 2
        elif args[i] == "--search-lang" and i + 1 < len(args):
            search_lang = args[i + 1]
            i += 2
        elif args[i] == "--result-filter" and i + 1 < len(args):
            result_filter = args[i + 1]
            i += 2
        else:
            i += 1

    # If no Brave API key, use DuckDuckGo fallback
    if not api_key:
        print("[No Brave API key — using DuckDuckGo fallback]")
        ddgs_search(query, count)
        sys.exit(0)

    # When freshness is specified, skip LLM Context (it doesn't support time filtering)
    # and go directly to web search which properly supports freshness.
    if freshness:
        return fallback_web_search(api_key, query, count, freshness, search_lang, result_filter)

    # Use LLM Context endpoint — optimized for AI, returns actual page content
    params = {
        "q": query,
        "count": str(count),
        "maximum_number_of_tokens": str(max_tokens),
    }
    url = f"https://api.search.brave.com/res/v1/llm/context?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        # Fallback to regular web search if LLM Context is not available
        if e.code in (401, 403, 404):
            print(f"[LLM Context unavailable (HTTP {e.code}), falling back to web search]")
            return fallback_web_search(api_key, query, count, freshness, search_lang, result_filter)
        print(f"Brave API error (HTTP {e.code}): {body}")
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    # Parse LLM Context response
    grounding = data.get("grounding", {})
    generic = grounding.get("generic", [])

    if not generic:
        print(f'No content found for: "{query}"')
        print("Falling back to web search...")
        return fallback_web_search(api_key, query, count, freshness, search_lang, result_filter)

    print(f'=== Search Results for: "{query}" ===\n')

    for idx, item in enumerate(generic, 1):
        title = item.get("title", "Unknown")
        url_str = item.get("url", "")
        snippets = item.get("snippets", [])

        print(f"--- Source {idx}: {title} ---")
        print(f"URL: {url_str}")
        if snippets:
            print("Content:")
            for snippet in snippets:
                # Clean up whitespace
                text = snippet.strip()
                if text:
                    print(f"  {text}")
        print()

    print(f"[{len(generic)} sources with content returned]")


def fallback_web_search(api_key, query, count, freshness="", search_lang="en", result_filter=""):
    """Fallback to regular web search with extra_snippets enabled."""
    params = {
        "q": query,
        "count": str(min(count, 10)),
        "extra_snippets": "true",
        "text_decorations": "false",
        "search_lang": search_lang,
    }
    if freshness:
        params["freshness"] = freshness
    if result_filter:
        params["result_filter"] = result_filter
    url = f"https://api.search.brave.com/res/v1/web/search?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except Exception as e:
        print(f"Web search also failed: {e}")
        sys.exit(1)

    results = data.get("web", {}).get("results", [])
    if not results:
        print(f'No results found for: "{query}"')
        return

    print(f'=== Web Search Results for: "{query}" ===\n')

    for i, r in enumerate(results, 1):
        title = r.get("title", "")
        link = r.get("url", "")
        desc = r.get("description", "")
        age = r.get("page_age", "")
        extra = r.get("extra_snippets", [])

        print(f"--- Result {i}: {title} ---")
        print(f"URL: {link}")
        if age:
            print(f"Age: {age}")
        if desc:
            print(f"Description: {desc}")
        if extra:
            print("Additional content:")
            for snippet in extra:
                print(f"  {snippet}")
        print()


if __name__ == "__main__":
    main()
