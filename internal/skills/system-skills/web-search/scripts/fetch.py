#!/usr/bin/env python3
"""Web page content fetcher — reads a URL and extracts clean text content.

Usage:
    python3 fetch.py "https://example.com/article" [--max-chars N]

Options:
    --max-chars N  Maximum characters to return (default: 12000)

No API key required — uses direct HTTP fetch + HTML text extraction.
"""
import html.parser
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


class HTMLTextExtractor(html.parser.HTMLParser):
    """Extract readable text from HTML, skipping scripts/styles/nav."""

    SKIP_TAGS = {"script", "style", "noscript", "svg", "nav", "footer", "header", "aside"}
    BLOCK_TAGS = {"p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
                  "li", "tr", "br", "blockquote", "pre", "section", "article"}

    def __init__(self):
        super().__init__()
        self._text = []
        self._skip_depth = 0
        self._title = ""
        self._in_title = False

    def handle_starttag(self, tag, attrs):
        tag = tag.lower()
        if tag in self.SKIP_TAGS:
            self._skip_depth += 1
        if tag == "title":
            self._in_title = True
        if tag in self.BLOCK_TAGS and self._text and self._text[-1] != "\n":
            self._text.append("\n")

    def handle_endtag(self, tag):
        tag = tag.lower()
        if tag in self.SKIP_TAGS:
            self._skip_depth = max(0, self._skip_depth - 1)
        if tag == "title":
            self._in_title = False
        if tag in self.BLOCK_TAGS:
            self._text.append("\n")

    def handle_data(self, data):
        if self._in_title:
            self._title = data.strip()
        if self._skip_depth == 0:
            self._text.append(data)

    def get_text(self):
        raw = "".join(self._text)
        # Collapse whitespace within lines, preserve paragraph breaks
        lines = raw.split("\n")
        cleaned = []
        for line in lines:
            line = " ".join(line.split())
            if line:
                cleaned.append(line)
        return "\n\n".join(cleaned)

    def get_title(self):
        return self._title


def fetch_url(url, max_chars=12000):
    """Fetch URL and extract text content."""
    headers = {
        "User-Agent": "Mozilla/5.0 (compatible; TofiBot/1.0; +https://tofi.dev)",
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        "Accept-Language": "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7",
    }

    req = urllib.request.Request(url, headers=headers)

    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            content_type = resp.headers.get("Content-Type", "")

            # Handle JSON responses directly
            if "application/json" in content_type:
                raw = resp.read().decode("utf-8", errors="replace")
                try:
                    data = json.loads(raw)
                    text = json.dumps(data, indent=2, ensure_ascii=False)
                    return url, "JSON Document", text[:max_chars]
                except json.JSONDecodeError:
                    return url, "JSON Document", raw[:max_chars]

            # Handle plain text
            if "text/plain" in content_type:
                raw = resp.read().decode("utf-8", errors="replace")
                return url, "Text Document", raw[:max_chars]

            # Handle HTML
            raw_bytes = resp.read()

            # Try to detect encoding
            encoding = "utf-8"
            if "charset=" in content_type:
                charset = content_type.split("charset=")[-1].strip().split(";")[0]
                encoding = charset

            try:
                raw_html = raw_bytes.decode(encoding, errors="replace")
            except (LookupError, UnicodeDecodeError):
                raw_html = raw_bytes.decode("utf-8", errors="replace")

            # Extract text
            extractor = HTMLTextExtractor()
            try:
                extractor.feed(raw_html)
            except Exception:
                pass

            title = extractor.get_title() or "Untitled"
            text = extractor.get_text()

            # Truncate to max_chars
            if len(text) > max_chars:
                text = text[:max_chars] + "\n\n[... content truncated ...]"

            return url, title, text

    except urllib.error.HTTPError as e:
        return url, "Error", f"HTTP {e.code}: {e.reason}"
    except urllib.error.URLError as e:
        return url, "Error", f"URL Error: {e.reason}"
    except Exception as e:
        return url, "Error", f"Fetch failed: {str(e)}"


def main():
    args = sys.argv[1:]
    if not args or not args[0].strip() or args[0].startswith("--"):
        print('Usage: python3 fetch.py "https://example.com/page" [--max-chars N]')
        sys.exit(1)

    url = args[0].strip()
    max_chars = 12000

    i = 1
    while i < len(args):
        if args[i] == "--max-chars" and i + 1 < len(args):
            try:
                max_chars = max(1000, min(50000, int(args[i + 1])))
            except ValueError:
                pass
            i += 2
        else:
            i += 1

    # Validate URL
    parsed = urllib.parse.urlparse(url)
    if not parsed.scheme:
        url = "https://" + url
    elif parsed.scheme not in ("http", "https"):
        print(f"Error: Only http/https URLs are supported, got: {parsed.scheme}")
        sys.exit(1)

    fetched_url, title, content = fetch_url(url, max_chars)

    print(f"=== {title} ===")
    print(f"URL: {fetched_url}")
    print(f"Length: {len(content)} chars")
    print("---")
    print(content)


if __name__ == "__main__":
    main()
