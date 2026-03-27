#!/usr/bin/env python3
"""Brave Summarizer — get an AI-generated summary for a search query.

Usage:
    python3 summarize.py "query"

This performs a two-step process:
1. Web search with summary=1 to get a summarizer key
2. Call Summarizer API with that key to get an AI-generated summary

Environment:
    BRAVE_API_KEY — Brave Search API subscription token (required)
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def ddgs_search(query, count=5):
    """Fallback search using DuckDuckGo (no API key needed)."""
    try:
        from duckduckgo_search import DDGS
        with DDGS() as ddgs:
            results = list(ddgs.text(query, max_results=count))
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

    args = sys.argv[1:]
    if not args or not args[0].strip():
        print('Usage: python3 summarize.py "query"')
        sys.exit(1)

    query = args[0].strip()

    # If no Brave API key, use DuckDuckGo fallback
    if not api_key:
        print("[No Brave API key — using DuckDuckGo fallback (no AI summary available)]")
        ddgs_search(query)
        sys.exit(0)

    # Step 1: Web search with summary=1 to get summary key
    params = {
        "q": query,
        "summary": "1",
        "count": "5",
    }
    url = f"https://api.search.brave.com/res/v1/web/search?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"Search API error (HTTP {e.code}): {body}")
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    # Extract summary key
    summarizer = data.get("summarizer", {})
    summary_key = summarizer.get("key", "")

    if not summary_key:
        # No summary available — fall back to showing search results with extra_snippets
        print(f'[No AI summary available for "{query}", showing search results instead]\n')
        results = data.get("web", {}).get("results", [])
        for i, r in enumerate(results[:5], 1):
            title = r.get("title", "")
            desc = r.get("description", "")
            link = r.get("url", "")
            print(f"{i}. {title}")
            print(f"   URL: {link}")
            if desc:
                print(f"   {desc}")
            print()
        return

    # Step 2: Call Summarizer endpoint with the key
    sum_url = f"https://api.search.brave.com/res/v1/summarizer/search?key={urllib.parse.quote(summary_key)}"

    req2 = urllib.request.Request(sum_url)
    req2.add_header("Accept", "application/json")
    req2.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req2, timeout=20) as resp:
            sum_data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"Summarizer API error (HTTP {e.code}): {body}")
        print("Falling back to search results...")
        results = data.get("web", {}).get("results", [])
        for i, r in enumerate(results[:5], 1):
            print(f"{i}. {r.get('title', '')} — {r.get('description', '')}")
        return
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    # Parse summary response
    title = sum_data.get("title", query)
    summary_parts = sum_data.get("summary", [])

    print(f'=== AI Summary: "{title}" ===\n')

    # Extract summary text
    for part in summary_parts:
        if isinstance(part, dict):
            text = part.get("data", "")
            if text:
                print(text)
        elif isinstance(part, str):
            print(part)

    # Show enrichments if available
    enrichments = sum_data.get("enrichments", {})

    # Q&A pairs
    qa = enrichments.get("qa", [])
    if qa:
        print("\n--- Related Q&A ---")
        for item in qa[:3]:
            answer = item.get("answer", "")
            if answer:
                print(f"  Q: {item.get('question', 'N/A')}")
                print(f"  A: {answer}")
                print()

    # Context/sources
    context = enrichments.get("context", [])
    if context:
        print("\n--- Sources ---")
        for src in context[:5]:
            print(f"  • {src.get('title', '')} — {src.get('url', '')}")

    # Follow-up questions
    followups = sum_data.get("followups", [])
    if followups:
        print("\n--- Related Questions ---")
        for q in followups[:3]:
            print(f"  • {q}")


if __name__ == "__main__":
    main()
