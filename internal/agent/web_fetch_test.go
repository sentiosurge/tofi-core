package agent

import (
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		{"https://example.com", true},
		{"http://example.com/path", true},
		{"https://docs.python.org/3/library/asyncio.html", true},

		// Invalid
		{"ftp://example.com", false},            // not http/https
		{"example.com", false},                  // no protocol
		{"", false},                             // empty
		{"https://user:pass@example.com", false}, // credentials in URL
	}

	for _, tt := range tests {
		err := validateURL(tt.url)
		if tt.valid && err != nil {
			t.Errorf("validateURL(%q) should be valid, got error: %v", tt.url, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("validateURL(%q) should be invalid", tt.url)
		}
	}
}

func TestValidateURL_TooLong(t *testing.T) {
	longURL := "https://example.com/" + string(make([]byte, 2000))
	err := validateURL(longURL)
	if err == nil {
		t.Error("expected error for URL > 2000 chars")
	}
}

func TestExtractTextFromHTML(t *testing.T) {
	tests := []struct {
		name  string
		html  string
		want  []string // strings that should be in output
		notWant []string // strings that should NOT be in output
	}{
		{
			name: "simple paragraph",
			html: "<html><body><p>Hello World</p></body></html>",
			want: []string{"Hello World"},
		},
		{
			name: "strips scripts",
			html: `<html><body><p>Content</p><script>alert('xss')</script></body></html>`,
			want:    []string{"Content"},
			notWant: []string{"alert", "xss"},
		},
		{
			name: "strips styles",
			html: `<html><head><style>body{color:red}</style></head><body><p>Visible</p></body></html>`,
			want:    []string{"Visible"},
			notWant: []string{"color:red"},
		},
		{
			name: "strips nav and footer",
			html: `<nav>Menu Item</nav><main><p>Main Content</p></main><footer>Copyright 2024</footer>`,
			want:    []string{"Main Content"},
			notWant: []string{"Menu Item", "Copyright"},
		},
		{
			name: "decodes entities",
			html: `<p>Tom &amp; Jerry &lt;3 &quot;cats&quot;</p>`,
			want: []string{"Tom & Jerry", `"cats"`},
		},
		{
			name: "collapses whitespace",
			html: `<p>Line 1</p>


<p>Line 2</p>`,
			want: []string{"Line 1", "Line 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextFromHTML(tt.html)
			for _, w := range tt.want {
				if !contains(result, w) {
					t.Errorf("expected %q in output, got:\n%s", w, result)
				}
			}
			for _, nw := range tt.notWant {
				if contains(result, nw) {
					t.Errorf("did NOT expect %q in output, got:\n%s", nw, result)
				}
			}
		})
	}
}

func TestChromeAvailable(t *testing.T) {
	// Just verify it doesn't panic — result depends on environment
	_ = chromeAvailable()
}

func TestFetchURL_WithCache(t *testing.T) {
	cache := NewFetchCache()

	// Pre-populate cache
	cache.Set(FetchKey("https://cached.example.com"), "cached content here")

	result, err := FetchURL("https://cached.example.com", 1000, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "cached content here" {
		t.Errorf("expected cached content, got: %s", result)
	}
}

func TestFetchURL_InvalidURL(t *testing.T) {
	_, err := FetchURL("not-a-url", 0, nil)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}
