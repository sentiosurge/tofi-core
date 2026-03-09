#!/usr/bin/env python3
"""Brave Image Search — find images with source info and dimensions.

Usage:
    python3 images.py "query" [--count N] [--search-lang LANG] [--safesearch MODE]

Options:
    --count N          Number of results (default: 10, max: 200)
    --search-lang LANG Content language code (default: en)
    --safesearch MODE  off or strict (default: strict)

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
        print("Error: BRAVE_API_KEY environment variable is not set.")
        sys.exit(1)

    args = sys.argv[1:]
    if not args or not args[0].strip() or args[0].startswith("--"):
        print('Usage: python3 images.py "query" [--count N] [--search-lang LANG] [--safesearch MODE]')
        sys.exit(1)

    query = args[0].strip()
    count = 10
    search_lang = "en"
    safesearch = "strict"

    i = 1
    while i < len(args):
        if args[i] == "--count" and i + 1 < len(args):
            try:
                count = max(1, min(200, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        elif args[i] == "--search-lang" and i + 1 < len(args):
            search_lang = args[i + 1]
            i += 2
        elif args[i] == "--safesearch" and i + 1 < len(args):
            safesearch = args[i + 1]
            i += 2
        else:
            i += 1

    params = {
        "q": query,
        "count": str(count),
        "search_lang": search_lang,
        "safesearch": safesearch,
    }

    url = f"https://api.search.brave.com/res/v1/images/search?{urllib.parse.urlencode(params)}"

    req = urllib.request.Request(url)
    req.add_header("Accept", "application/json")
    req.add_header("X-Subscription-Token", api_key)

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"Brave Images API error (HTTP {e.code}): {body}")
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"Network error: {e.reason}")
        sys.exit(1)

    results = data.get("results", [])

    print(f'=== Images: "{query}" ===\n')

    if not results:
        print("No images found.")
        return

    for idx, img in enumerate(results, 1):
        title = img.get("title", "")
        page_url = img.get("url", "")
        source = img.get("source", "") or img.get("meta_url", {}).get("hostname", "")
        props = img.get("properties", {})
        img_url = props.get("url", "")
        width = props.get("width")
        height = props.get("height")

        print(f"--- [{idx}] {title} ---")
        if source:
            print(f"Source: {source}")
        if width and height:
            print(f"Size: {width}x{height}")
        print(f"Page: {page_url}")
        if img_url:
            print(f"Image: {img_url}")
        print()

    print(f"[{len(results)} images found]")


if __name__ == "__main__":
    main()
