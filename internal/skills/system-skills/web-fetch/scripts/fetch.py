#!/usr/bin/env python3
"""Web page content fetcher — uses headless Chrome to render JS and extract text.

Usage:
    python3 fetch.py "https://example.com/article" [--max-chars N]

Options:
    --max-chars N  Maximum characters to return (default: 12000)

Requires Google Chrome or Chromium installed on the system.
Uses trafilatura for content extraction if available (pip install trafilatura).
"""
import os
import platform
import re
import subprocess
import sys

try:
    import trafilatura
    USE_TRAFILATURA = True
except ImportError:
    USE_TRAFILATURA = False

CHROME_PATHS = {
    "Darwin": [
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    ],
    "Linux": [
        "/usr/bin/google-chrome",
        "/usr/bin/google-chrome-stable",
        "/usr/bin/chromium-browser",
        "/usr/bin/chromium",
        "/snap/bin/chromium",
    ],
    "Windows": [
        r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    ],
}


def find_chrome():
    """Find Chrome/Chromium binary. Returns path or None."""
    system = platform.system()

    # Check known paths first
    for path in CHROME_PATHS.get(system, []):
        if os.path.isfile(path):
            return path

    # Search common names via which/where
    for name in ("google-chrome", "google-chrome-stable", "chromium", "chromium-browser"):
        try:
            result = subprocess.run(
                ["which", name] if system != "Windows" else ["where", name],
                capture_output=True, text=True, timeout=5
            )
            if result.returncode == 0 and result.stdout.strip():
                return result.stdout.strip().split("\n")[0]
        except Exception:
            pass

    return None


def fetch_with_chrome(url, chrome_path, max_chars=12000):
    """Fetch a URL using headless Chrome --dump-dom, then extract text."""
    # Use a realistic User-Agent to avoid 403 blocks
    user_agent = (
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
    )

    try:
        result = subprocess.run(
            [
                chrome_path,
                "--headless=new",
                "--dump-dom",
                "--no-sandbox",
                "--disable-gpu",
                "--disable-extensions",
                "--disable-background-networking",
                "--disable-blink-features=AutomationControlled",
                f"--user-agent={user_agent}",
                "--timeout=15000",
                url,
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        html = result.stdout
    except subprocess.TimeoutExpired:
        return url, "Error", f"Timeout fetching {url} (30s)"
    except Exception as e:
        return url, "Error", f"Chrome failed: {e}"

    if not html.strip():
        stderr_hint = result.stderr[:200] if result.stderr else "no output"
        return url, "Error", f"Empty response from Chrome. stderr: {stderr_hint}"

    # Extract title from HTML
    title = "Untitled"
    title_match = re.search(r"<title[^>]*>(.*?)</title>", html, re.IGNORECASE | re.DOTALL)
    if title_match:
        title = title_match.group(1).strip()
        # Decode HTML entities
        title = re.sub(r"&amp;", "&", title)
        title = re.sub(r"&lt;", "<", title)
        title = re.sub(r"&gt;", ">", title)
        title = re.sub(r"&#(\d+);", lambda m: chr(int(m.group(1))), title)

    # Extract text content
    text = extract_text(html)

    if len(text) > max_chars:
        text = text[:max_chars] + "\n\n[... content truncated ...]"

    return url, title, text


def extract_text(html):
    """Extract clean text from HTML. Uses trafilatura if available, else regex fallback."""
    if USE_TRAFILATURA:
        text = trafilatura.extract(html, include_comments=False, include_tables=True)
        if text and len(text) > 100:
            return text

    # Fallback: regex-based extraction
    # Remove scripts, styles, and hidden elements
    text = re.sub(r"<script[^>]*>.*?</script>", "", html, flags=re.DOTALL | re.IGNORECASE)
    text = re.sub(r"<style[^>]*>.*?</style>", "", text, flags=re.DOTALL | re.IGNORECASE)
    text = re.sub(r"<noscript[^>]*>.*?</noscript>", "", text, flags=re.DOTALL | re.IGNORECASE)
    text = re.sub(r"<!--.*?-->", "", text, flags=re.DOTALL)

    # Convert block elements to newlines
    text = re.sub(r"<(?:p|div|h[1-6]|li|tr|br|blockquote|pre|section|article)[^>]*>", "\n", text, flags=re.IGNORECASE)

    # Strip remaining tags
    text = re.sub(r"<[^>]+>", " ", text)

    # Decode common HTML entities
    text = re.sub(r"&nbsp;", " ", text)
    text = re.sub(r"&amp;", "&", text)
    text = re.sub(r"&lt;", "<", text)
    text = re.sub(r"&gt;", ">", text)
    text = re.sub(r"&quot;", '"', text)
    text = re.sub(r"&#(\d+);", lambda m: chr(int(m.group(1))), text)

    # Clean up whitespace
    lines = text.split("\n")
    cleaned = []
    for line in lines:
        line = " ".join(line.split())
        if line:
            cleaned.append(line)
    return "\n\n".join(cleaned)


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

    # Auto-add https:// if no scheme
    if not url.startswith("http://") and not url.startswith("https://"):
        url = "https://" + url

    # Find Chrome
    chrome_path = find_chrome()
    if not chrome_path:
        print("ERROR: Google Chrome or Chromium not found.", file=sys.stderr)
        print("Install Chrome/Chromium to use web-fetch:", file=sys.stderr)
        print("  macOS:  brew install --cask google-chrome", file=sys.stderr)
        print("  Ubuntu: sudo apt install chromium-browser", file=sys.stderr)
        sys.exit(1)

    fetched_url, title, content = fetch_with_chrome(url, chrome_path, max_chars)

    print(f"=== {title} ===")
    print(f"URL: {fetched_url}")
    print(f"Length: {len(content)} chars")
    if USE_TRAFILATURA:
        print("Extractor: trafilatura")
    else:
        print("Extractor: regex (install trafilatura for better results)")
    print("---")
    print(content)


if __name__ == "__main__":
    main()
