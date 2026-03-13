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

// anthropicProvider implements Provider using the Anthropic Messages API.
type anthropicProvider struct {
	apiKey  string
	baseURL string
}

func newAnthropic(apiKey string, cfg *providerConfig) (Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Anthropic API key is required")
	}
	baseURL := "https://api.anthropic.com"
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	return &anthropicProvider{apiKey: apiKey, baseURL: baseURL}, nil
}

// Chat sends a non-streaming request to Anthropic Messages API.
func (a *anthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	payload := a.buildPayload(req, false)
	body, err := a.doRequest(ctx, payload)
	if err != nil {
		return nil, err
	}
	return a.parseResponse(body)
}

// ChatStream sends a streaming request to Anthropic Messages API.
func (a *anthropicProvider) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error) {
	payload := a.buildPayload(req, true)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	a.setHeaders(httpReq)

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

	return a.parseStream(resp.Body, onDelta)
}

func (a *anthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

// buildPayload constructs the Anthropic Messages API request.
func (a *anthropicProvider) buildPayload(req *ChatRequest, stream bool) map[string]interface{} {
	payload := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": 8192,
	}

	// System prompt is a top-level field in Anthropic
	if req.System != "" {
		payload["system"] = req.System
	}

	if stream {
		payload["stream"] = true
	}

	// Convert messages to Anthropic format
	messages := a.convertMessages(req.Messages)
	payload["messages"] = messages

	// Convert tools to Anthropic format
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			tool := map[string]interface{}{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			}
			tools = append(tools, tool)
		}
		payload["tools"] = tools
	}

	return payload
}

// convertMessages converts unified Messages to Anthropic format.
// Key differences:
// - No "system" role in messages (handled separately)
// - Tool calls are content blocks of type "tool_use" in assistant messages
// - Tool results are content blocks of type "tool_result" in user messages
// - Consecutive same-role messages must be merged
func (a *anthropicProvider) convertMessages(msgs []Message) []map[string]interface{} {
	var result []map[string]interface{}

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})

		case "assistant":
			// Build content blocks
			var content []map[string]interface{}
			if msg.Content != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": msg.Content,
				})
			}
			// Tool calls become tool_use content blocks
			for _, tc := range msg.ToolCalls {
				var input interface{}
				if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
					input = map[string]interface{}{}
				}
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": input,
				})
			}
			if len(content) > 0 {
				result = append(result, map[string]interface{}{
					"role":    "assistant",
					"content": content,
				})
			}

		case "tool":
			// Tool result → user message with tool_result content block
			// Anthropic requires tool_result to be inside a user message
			toolResult := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": msg.ToolCallID,
				"content":     msg.Content,
			}

			// Try to merge with previous user message (if it's also tool results)
			if len(result) > 0 {
				last := result[len(result)-1]
				if lastRole, ok := last["role"].(string); ok && lastRole == "user" {
					// Check if last message has content as array (already has tool_results)
					if lastContent, ok := last["content"].([]map[string]interface{}); ok {
						last["content"] = append(lastContent, toolResult)
						continue
					}
				}
			}

			// New user message with tool_result
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": []map[string]interface{}{toolResult},
			})
		}
	}

	return result
}

func (a *anthropicProvider) doRequest(ctx context.Context, payload map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", strings.NewReader(string(jsonData)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	a.setHeaders(httpReq)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return string(respBody), nil
}

// parseResponse parses a non-streaming Anthropic Messages API response.
func (a *anthropicProvider) parseResponse(body string) (*ChatResponse, error) {
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error [%s]: %s", resp.Error.Type, resp.Error.Message)
	}

	result := &ChatResponse{
		Usage: Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			args := string(block.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		case "thinking":
			result.Reasoning += block.Text
		}
	}

	return result, nil
}

// parseStream parses Anthropic's streaming format.
// Anthropic uses event: + data: lines with specific event types:
// message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop
func (a *anthropicProvider) parseStream(body io.Reader, onDelta func(StreamDelta)) (*ChatResponse, error) {
	result := &ChatResponse{}
	var contentBuf strings.Builder

	// Track content blocks by index
	type blockInfo struct {
		Type string // "text" or "tool_use"
		ID   string
		Name string
		Args strings.Builder
	}
	blocks := make(map[int]*blockInfo)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch currentEvent {
		case "message_start":
			// Extract initial usage
			var ev struct {
				Message struct {
					Usage struct {
						InputTokens int64 `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				result.Usage.InputTokens = ev.Message.Usage.InputTokens
			}

		case "content_block_start":
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				blocks[ev.Index] = &blockInfo{
					Type: ev.ContentBlock.Type,
					ID:   ev.ContentBlock.ID,
					Name: ev.ContentBlock.Name,
				}
				// Notify about new tool call
				if ev.ContentBlock.Type == "tool_use" && onDelta != nil {
					onDelta(StreamDelta{
						ToolCalls: []ToolCallDelta{{
							Index: ev.Index,
							ID:    ev.ContentBlock.ID,
							Name:  ev.ContentBlock.Name,
						}},
					})
				}
			}

		case "content_block_delta":
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				block := blocks[ev.Index]

				switch ev.Delta.Type {
				case "text_delta":
					contentBuf.WriteString(ev.Delta.Text)
					if onDelta != nil {
						onDelta(StreamDelta{Content: ev.Delta.Text})
					}

				case "input_json_delta":
					if block != nil {
						block.Args.WriteString(ev.Delta.PartialJSON)
						if onDelta != nil {
							onDelta(StreamDelta{
								ToolCalls: []ToolCallDelta{{
									Index:     ev.Index,
									Arguments: ev.Delta.PartialJSON,
								}},
							})
						}
					}

				case "thinking_delta":
					if onDelta != nil {
						onDelta(StreamDelta{Reasoning: ev.Delta.Text})
					}
				}
			}

		case "message_delta":
			// Final usage update
			var ev struct {
				Usage struct {
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				result.Usage.OutputTokens = ev.Usage.OutputTokens
			}

		case "error":
			var ev struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				return nil, fmt.Errorf("stream error [%s]: %s", ev.Error.Type, ev.Error.Message)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = contentBuf.String()

	// Assemble tool calls from blocks
	for i := 0; ; i++ {
		block, ok := blocks[i]
		if !ok {
			break
		}
		if block.Type == "tool_use" {
			args := block.Args.String()
			if args == "" {
				args = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}

	return result, nil
}
