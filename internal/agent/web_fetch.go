package agent

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// ──────────────────────────────────────────────────────────────
// Web Fetch Go HTTP Fallback
//
// The primary web fetch is the system skill (Python + headless Chrome).
// This Go implementation provides a lightweight fallback when:
// - Chrome is not installed
// - Simple static pages (no JS rendering needed)
// - Called from a context where Python isn't available
//
// Features borrowed from Claude Code:
// - 60s timeout
// - 10MB max response
// - HTML → plain text conversion (basic, no Turndown)
// - URL validation
// ──────────────────────────────────────────────────────────────

const (
	fetchTimeout       = 60 * time.Second
	fetchMaxBytes      = 10 * 1024 * 1024 // 10MB
	fetchMaxOutputLen  = 50000            // chars returned to agent
)

// FetchURL fetches a URL using Go's net/http and returns clean text.
// Falls back from Chrome to Go HTTP if Chrome is not available.
func FetchURL(url string, maxChars int, fetchCache *WebCache) (string, error) {
	if maxChars <= 0 {
		maxChars = fetchMaxOutputLen
	}

	// Validate URL
	if err := validateURL(url); err != nil {
		return "", err
	}

	// Check cache
	if fetchCache != nil {
		cacheKey := FetchKey(url)
		if cached, ok := fetchCache.Get(cacheKey); ok {
			return cached, nil
		}
	}

	// Try Chrome first (JS rendering), fallback to Go HTTP
	var content string
	var err error

	if chromeAvailable() {
		content, err = fetchWithChrome(url)
	}
	if content == "" || err != nil {
		content, err = fetchWithHTTP(url)
	}
	if err != nil {
		return "", err
	}

	// Truncate
	if len(content) > maxChars {
		content = smartTruncate(content, maxChars)
	}

	// Cache result
	if fetchCache != nil {
		fetchCache.Set(FetchKey(url), content)
	}

	return content, nil
}

func validateURL(url string) error {
	if len(url) > 2000 {
		return fmt.Errorf("URL too long (%d chars, max 2000)", len(url))
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	if strings.Contains(url, "@") {
		return fmt.Errorf("URLs with credentials not allowed")
	}
	return nil
}

// chromeAvailable checks if Chrome/Chromium is installed.
func chromeAvailable() bool {
	paths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"google-chrome",
		"chromium",
		"chromium-browser",
	}
	for _, p := range paths {
		if _, err := exec.LookPath(p); err == nil {
			return true
		}
	}
	// Check macOS app path directly
	for _, p := range paths[:2] {
		if fileExists(p) {
			return true
		}
	}
	return false
}

// fetchWithChrome uses headless Chrome to render and dump DOM.
func fetchWithChrome(url string) (string, error) {
	chromePath := findChrome()
	if chromePath == "" {
		return "", fmt.Errorf("chrome not found")
	}

	cmd := exec.Command(chromePath,
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--dump-dom",
		"--timeout=30000",
		url,
	)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("chrome fetch failed: %w", err)
	}

	html := string(output)
	return extractTextFromHTML(html), nil
}

// fetchWithHTTP uses Go's net/http as a lightweight fallback.
func fetchWithHTTP(url string) (string, error) {
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "Tofi/1.0 (+https://tofi.sh)")
	req.Header.Set("Accept", "text/html, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(fetchMaxBytes)))
	if err != nil {
		return "", fmt.Errorf("read body failed: %w", err)
	}

	// Check if binary
	if !utf8.Valid(body) {
		return fmt.Sprintf("[Binary content: %s, %d bytes]", resp.Header.Get("Content-Type"), len(body)), nil
	}

	content := string(body)

	// If HTML, extract text
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		content = extractTextFromHTML(content)
	}

	return content, nil
}

// extractTextFromHTML does basic HTML → text conversion (no external dependency).
// Not as good as trafilatura but works as a fallback.
func extractTextFromHTML(html string) string {
	// Remove script and style blocks
	html = removeTagBlock(html, "script")
	html = removeTagBlock(html, "style")
	html = removeTagBlock(html, "nav")
	html = removeTagBlock(html, "footer")
	html = removeTagBlock(html, "header")

	// Remove all HTML tags
	var result strings.Builder
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			result.WriteRune(' ')
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}

	// Clean up whitespace
	text := result.String()
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")

	// Collapse multiple whitespace/newlines
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	return strings.Join(cleaned, "\n")
}

func removeTagBlock(html, tag string) string {
	lower := strings.ToLower(html)
	for {
		start := strings.Index(lower, "<"+tag)
		if start == -1 {
			break
		}
		end := strings.Index(lower[start:], "</"+tag+">")
		if end == -1 {
			// Unclosed tag — remove to end
			html = html[:start]
			lower = lower[:start]
			break
		}
		endPos := start + end + len("</"+tag+">")
		html = html[:start] + html[endPos:]
		lower = lower[:start] + lower[endPos:]
	}
	return html
}

func findChrome() string {
	// macOS paths
	macPaths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, p := range macPaths {
		if fileExists(p) {
			return p
		}
	}

	// Linux — look in PATH
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	return ""
}

func fileExists(path string) bool {
	_, err := exec.LookPath(path)
	if err == nil {
		return true
	}
	// For absolute paths, try stat
	if strings.HasPrefix(path, "/") {
		_, statErr := statFile(path)
		return statErr == nil
	}
	return false
}

func statFile(path string) (interface{}, error) {
	// Use os.Stat indirectly to avoid import cycle issues in test
	cmd := exec.Command("test", "-f", path)
	return nil, cmd.Run()
}
