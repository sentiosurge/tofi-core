package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// geminiProvider implements Provider using the Google Gemini API.
type geminiProvider struct {
	apiKey  string
	baseURL string
}

func newGemini(apiKey string, cfg *providerConfig) (Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Gemini API key is required")
	}
	baseURL := "https://generativelanguage.googleapis.com/v1beta"
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	return &geminiProvider{apiKey: apiKey, baseURL: baseURL}, nil
}

// Chat sends a non-streaming request to Gemini generateContent.
func (g *geminiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	payload := g.buildPayload(req)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", g.baseURL, req.Model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return g.parseResponse(respBody)
}

// ChatStream sends a streaming request to Gemini streamGenerateContent.
func (g *geminiProvider) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error) {
	payload := g.buildPayload(req)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", g.baseURL, req.Model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return g.parseStream(resp.Body, onDelta)
}

// buildPayload constructs a Gemini generateContent request.
func (g *geminiProvider) buildPayload(req *ChatRequest) map[string]interface{} {
	payload := map[string]interface{}{}

	// System instruction is a separate top-level field
	if req.System != "" {
		payload["system_instruction"] = map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": req.System},
			},
		}
	}

	// Convert messages to Gemini contents format
	payload["contents"] = g.convertMessages(req.Messages)

	// Convert tools to Gemini format
	if len(req.Tools) > 0 {
		var funcDecls []map[string]interface{}
		for _, t := range req.Tools {
			decl := map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
			}
			if t.Parameters != nil {
				decl["parameters"] = t.Parameters
			}
			funcDecls = append(funcDecls, decl)
		}
		payload["tools"] = []map[string]interface{}{
			{"function_declarations": funcDecls},
		}
	}

	// Generation config
	payload["generationConfig"] = map[string]interface{}{
		"maxOutputTokens": 8192,
	}

	return payload
}

// convertMessages converts unified Messages to Gemini contents format.
// Key differences:
// - Roles are "user" and "model" (not "assistant")
// - Tool calls are functionCall parts in model messages
// - Tool results are functionResponse parts in user messages
// - Consecutive same-role messages must be merged
func (g *geminiProvider) convertMessages(msgs []Message) []map[string]interface{} {
	var contents []map[string]interface{}

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			content := map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": msg.Content},
				},
			}
			// Try merge with previous user content
			if len(contents) > 0 {
				last := contents[len(contents)-1]
				if lastRole, ok := last["role"].(string); ok && lastRole == "user" {
					if lastParts, ok := last["parts"].([]map[string]interface{}); ok {
						last["parts"] = append(lastParts, map[string]interface{}{"text": msg.Content})
						continue
					}
				}
			}
			contents = append(contents, content)

		case "assistant":
			var parts []map[string]interface{}
			if msg.Content != "" {
				parts = append(parts, map[string]interface{}{
					"text": msg.Content,
				})
			}
			// Tool calls become functionCall parts
			for _, tc := range msg.ToolCalls {
				var args interface{}
				if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
					args = map[string]interface{}{}
				}
				parts = append(parts, map[string]interface{}{
					"functionCall": map[string]interface{}{
						"name": tc.Name,
						"args": args,
					},
				})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]interface{}{
					"role":  "model",
					"parts": parts,
				})
			}

		case "tool":
			// Tool result → user message with functionResponse part
			var resultObj interface{}
			// Try to parse as JSON; if it fails, wrap as string
			if err := json.Unmarshal([]byte(msg.Content), &resultObj); err != nil {
				resultObj = map[string]interface{}{"result": msg.Content}
			}

			funcResponse := map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name":     msg.ToolName,
					"response": resultObj,
				},
			}

			// Try to merge with previous user message (consecutive tool results)
			if len(contents) > 0 {
				last := contents[len(contents)-1]
				if lastRole, ok := last["role"].(string); ok && lastRole == "user" {
					if lastParts, ok := last["parts"].([]map[string]interface{}); ok {
						last["parts"] = append(lastParts, funcResponse)
						continue
					}
				}
			}

			contents = append(contents, map[string]interface{}{
				"role":  "user",
				"parts": []map[string]interface{}{funcResponse},
			})
		}
	}

	return contents
}

// parseResponse parses a non-streaming Gemini generateContent response.
func (g *geminiProvider) parseResponse(body []byte) (*ChatResponse, error) {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string                 `json:"name"`
						Args map[string]interface{} `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error [%s]: %s", resp.Error.Status, resp.Error.Message)
	}

	result := &ChatResponse{
		Usage: Usage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		},
	}

	if len(resp.Candidates) > 0 {
		var textParts []string
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.FunctionCall != nil {
				argsJSON, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					argsJSON = []byte("{}")
				}
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					// Gemini doesn't use call IDs; generate one from name + index
					ID:        fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, len(result.ToolCalls)),
					Name:      part.FunctionCall.Name,
					Arguments: string(argsJSON),
				})
			}
		}
		result.Content = strings.Join(textParts, "")
	}

	return result, nil
}

// parseStream parses Gemini's SSE streaming format.
// Each SSE event contains a complete candidate delta with parts.
func (g *geminiProvider) parseStream(body io.Reader, onDelta func(StreamDelta)) (*ChatResponse, error) {
	result := &ChatResponse{}
	var contentBuf strings.Builder
	toolCallIndex := 0

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text         string `json:"text"`
						FunctionCall *struct {
							Name string                 `json:"name"`
							Args map[string]interface{} `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int64 `json:"promptTokenCount"`
				CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Candidates) > 0 {
			for _, part := range chunk.Candidates[0].Content.Parts {
				if part.Text != "" {
					contentBuf.WriteString(part.Text)
					if onDelta != nil {
						onDelta(StreamDelta{Content: part.Text})
					}
				}
				if part.FunctionCall != nil {
					argsJSON, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						argsJSON = []byte("{}")
					}
					callID := fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, toolCallIndex)
					result.ToolCalls = append(result.ToolCalls, ToolCall{
						ID:        callID,
						Name:      part.FunctionCall.Name,
						Arguments: string(argsJSON),
					})
					if onDelta != nil {
						onDelta(StreamDelta{
							ToolCalls: []ToolCallDelta{{
								Index:     toolCallIndex,
								ID:        callID,
								Name:      part.FunctionCall.Name,
								Arguments: string(argsJSON),
							}},
						})
					}
					toolCallIndex++
				}
			}
		}

		// Usage (typically in the last chunk)
		if chunk.UsageMetadata != nil {
			result.Usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
			result.Usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = contentBuf.String()
	return result, nil
}
