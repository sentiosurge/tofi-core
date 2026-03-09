package capability

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tofi-core/internal/mcp"
)

// BuildWebSearchTool creates an ExtraBuiltinTool for web search via Brave Search API.
func BuildWebSearchTool(apiKey string) mcp.ExtraBuiltinTool {
	return mcp.ExtraBuiltinTool{
		Schema: mcp.OpenAITool{
			Type: "function",
			Function: mcp.OpenAIFunctionDef{
				Name:        "web_search",
				Description: "Search the web for real-time information using Brave Search API. Use this when you need current data, news, prices, or any information that may have changed recently.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The search query",
						},
						"count": map[string]any{
							"type":        "integer",
							"description": "Number of results to return (1-10, default 5)",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "Error: query is required", nil
			}
			count := 5
			if c, ok := args["count"].(float64); ok && c >= 1 && c <= 10 {
				count = int(c)
			}
			return braveSearch(apiKey, query, count)
		},
	}
}

// braveSearch calls the Brave Search API and returns formatted results.
func braveSearch(apiKey, query string, count int) (string, error) {
	reqURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("Search failed: %v", err), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("Failed to read response: %v", err), nil
	}

	if resp.StatusCode != 200 {
		return fmt.Sprintf("Brave Search API error (HTTP %d): %s", resp.StatusCode, string(body)), nil
	}

	// Parse the response
	var result braveSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Failed to parse response: %v", err), nil
	}

	// Format results
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for: %q\n\n", query))

	if result.Web == nil || len(result.Web.Results) == 0 {
		sb.WriteString("No results found.")
		return sb.String(), nil
	}

	for i, r := range result.Web.Results {
		if i >= count {
			break
		}
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("   URL: %s\n", r.URL))
		if r.Description != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.Description))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

type braveSearchResponse struct {
	Web *braveWebResults `json:"web"`
}

type braveWebResults struct {
	Results []braveWebResult `json:"results"`
}

type braveWebResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}
