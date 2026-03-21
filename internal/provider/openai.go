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

// openaiResponses implements Provider using the OpenAI Responses API.
// This is the primary API for all OpenAI native models.
// It includes an automatic fallback to Chat Completions API when the
// Responses API fails with tool-related errors (known API issue).
type openaiResponses struct {
	apiKey  string
	baseURL string
	legacy  *openaiLegacy // Chat Completions fallback for tool-related errors
}

func newOpenAIResponses(apiKey string, cfg *providerConfig) (Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}
	baseURL := "https://api.openai.com/v1"
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	// Create a Chat Completions fallback for when Responses API has tool issues
	legacy := &openaiLegacy{apiKey: apiKey, baseURL: baseURL}
	return &openaiResponses{apiKey: apiKey, baseURL: baseURL, legacy: legacy}, nil
}

// isToolCallError checks if the error is a Responses API tool-related error
// that can be retried via the Chat Completions API fallback.
func isToolCallError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No tool output found for function call") ||
		strings.Contains(msg, "tool output") ||
		(strings.Contains(msg, "HTTP 400") && strings.Contains(msg, "function_call"))
}

// Chat sends a non-streaming request via the Responses API.
// Falls back to Chat Completions if the Responses API fails with tool errors.
func (o *openaiResponses) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	payload := o.buildPayload(req, false)

	body, err := o.doRequest(ctx, payload)
	if err != nil {
		// Fallback to Chat Completions API for tool-related errors
		if isToolCallError(err) && len(req.Tools) > 0 {
			return o.legacy.Chat(ctx, req)
		}
		return nil, err
	}

	return o.parseResponse(body)
}

// ChatStream sends a streaming request via the Responses API.
// Falls back to Chat Completions if the Responses API fails with tool errors.
func (o *openaiResponses) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error) {
	payload := o.buildPayload(req, true)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/responses", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		httpErr := fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
		// Fallback to Chat Completions API for tool-related errors
		if isToolCallError(httpErr) && len(req.Tools) > 0 {
			return o.legacy.ChatStream(ctx, req, onDelta)
		}
		return nil, httpErr
	}

	return o.parseStream(resp.Body, onDelta)
}

// buildPayload constructs the Responses API request body.
func (o *openaiResponses) buildPayload(req *ChatRequest, stream bool) map[string]interface{} {
	payload := map[string]interface{}{
		"model": req.Model,
	}

	if req.System != "" {
		payload["instructions"] = req.System
	}

	if stream {
		payload["stream"] = true
	}

	// Enable reasoning with summary for models that support it
	payload["reasoning"] = map[string]interface{}{
		"effort":  "medium",
		"summary": "auto",
	}

	// Convert messages to Responses API input format
	input := o.convertMessages(req.Messages)
	if len(input) > 0 {
		payload["input"] = input
	}

	// Convert tools to Responses API format (flat, no function wrapper)
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			tool := map[string]interface{}{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
				"strict":      false, // Don't enforce strict mode for flexibility
			}
			tools = append(tools, tool)
		}
		payload["tools"] = tools
	}

	return payload
}

// convertMessages converts unified Messages to Responses API input format.
func (o *openaiResponses) convertMessages(msgs []Message) []interface{} {
	var input []interface{}

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			input = append(input, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant message with tool calls becomes multiple output items
				// First, add text content if any
				if msg.Content != "" {
					input = append(input, map[string]interface{}{
						"type": "message",
						"role": "assistant",
						"content": []map[string]interface{}{
							{"type": "output_text", "text": msg.Content},
						},
					})
				}
				// Then add function_call items
				// Note: only set call_id (not id) — id is the item's unique identifier
				// which we don't preserve from the original response. Setting id to the
				// wrong value can confuse the Responses API validation.
				for _, tc := range msg.ToolCalls {
					input = append(input, map[string]interface{}{
						"type":      "function_call",
						"call_id":   tc.ID,
						"name":      tc.Name,
						"arguments": tc.Arguments,
						"status":    "completed",
					})
				}
			} else if msg.Content != "" {
				input = append(input, map[string]interface{}{
					"type": "message",
					"role": "assistant",
					"content": []map[string]interface{}{
						{"type": "output_text", "text": msg.Content},
					},
				})
			}

		case "tool":
			// Tool result → function_call_output
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.Content,
				"status":  "completed",
			})
		}
	}

	return input
}

// doRequest sends a non-streaming POST request.
func (o *openaiResponses) doRequest(ctx context.Context, payload map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/responses", strings.NewReader(string(jsonData)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

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

// parseResponse parses a non-streaming Responses API response.
func (o *openaiResponses) parseResponse(body string) (*ChatResponse, error) {
	result := &ChatResponse{}

	// Parse output items
	var resp struct {
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result.Usage.InputTokens = resp.Usage.InputTokens
	result.Usage.OutputTokens = resp.Usage.OutputTokens

	for _, raw := range resp.Output {
		var item struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Args    string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}

		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					result.Content += c.Text
				}
			}
		case "function_call":
			callID := item.ID
			if callID == "" {
				callID = item.CallID
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        callID,
				Name:      item.Name,
				Arguments: item.Args,
			})
		case "reasoning":
			// Extract reasoning summary if present
			for _, c := range item.Content {
				if c.Type == "summary_text" {
					result.Reasoning += c.Text
				}
			}
		}
	}

	return result, nil
}

// parseStream parses the Responses API streaming events.
func (o *openaiResponses) parseStream(body io.Reader, onDelta func(StreamDelta)) (*ChatResponse, error) {
	result := &ChatResponse{}
	var contentBuf strings.Builder
	var reasoningBuf strings.Builder

	// Track function calls by output_index
	type fcAccum struct {
		ID   string
		Name string
		Args strings.Builder
	}
	fcMap := make(map[int]*fcAccum)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		// Track event type
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch currentEvent {
		case "response.output_text.delta":
			var ev struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta != "" {
				contentBuf.WriteString(ev.Delta)
				if onDelta != nil {
					onDelta(StreamDelta{Content: ev.Delta})
				}
			}

		case "response.reasoning_summary_text.delta":
			var ev struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta != "" {
				reasoningBuf.WriteString(ev.Delta)
				if onDelta != nil {
					onDelta(StreamDelta{Reasoning: ev.Delta})
				}
			}

		case "response.output_item.added":
			// A new output item — could be function_call or reasoning
			var ev struct {
				OutputIndex int `json:"output_index"`
				Item        struct {
					Type   string `json:"type"`
					ID     string `json:"id"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Item.Type == "function_call" {
				callID := ev.Item.ID
				if callID == "" {
					callID = ev.Item.CallID
				}
				fcMap[ev.OutputIndex] = &fcAccum{
					ID:   callID,
					Name: ev.Item.Name,
				}
				if onDelta != nil {
					onDelta(StreamDelta{
						ToolCalls: []ToolCallDelta{{
							Index: ev.OutputIndex,
							ID:    callID,
							Name:  ev.Item.Name,
						}},
					})
				}
			}

		case "response.function_call_arguments.delta":
			var ev struct {
				OutputIndex int    `json:"output_index"`
				Delta       string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				if acc, ok := fcMap[ev.OutputIndex]; ok {
					acc.Args.WriteString(ev.Delta)
					if onDelta != nil {
						onDelta(StreamDelta{
							ToolCalls: []ToolCallDelta{{
								Index:     ev.OutputIndex,
								Arguments: ev.Delta,
							}},
						})
					}
				}
			}

		case "response.output_item.done":
			// Check for reasoning item with summary — only use as fallback
			// if we didn't already receive reasoning via streaming deltas.
			var ev struct {
				Item json.RawMessage `json:"item"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				var item struct {
					Type    string `json:"type"`
					Summary []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"summary"`
				}
				if json.Unmarshal(ev.Item, &item) == nil && item.Type == "reasoning" {
					// Only use summary from done event if no streaming deltas were received
					if reasoningBuf.Len() == 0 {
						for _, s := range item.Summary {
							if s.Text != "" {
								reasoningBuf.WriteString(s.Text)
								if onDelta != nil {
									onDelta(StreamDelta{Reasoning: s.Text})
								}
							}
						}
					}
				}
			}

		case "response.completed":
			// Final event with usage
			var ev struct {
				Response struct {
					Usage struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				result.Usage.InputTokens = ev.Response.Usage.InputTokens
				result.Usage.OutputTokens = ev.Response.Usage.OutputTokens
			}

		case "response.failed":
			var ev struct {
				Response struct {
					Error struct {
						Message string `json:"message"`
						Code    string `json:"code"`
					} `json:"error"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				return nil, fmt.Errorf("response failed: [%s] %s", ev.Response.Error.Code, ev.Response.Error.Message)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = contentBuf.String()
	result.Reasoning = reasoningBuf.String()

	// Assemble tool calls — iterate by sorted output_index keys
	// (output_index may not start at 0 if text/reasoning items precede tool calls)
	if len(fcMap) > 0 {
		// Find the max output_index to iterate over all possible indices
		maxIdx := 0
		for idx := range fcMap {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if acc, ok := fcMap[i]; ok {
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        acc.ID,
					Name:      acc.Name,
					Arguments: acc.Args.String(),
				})
			}
		}
	}

	return result, nil
}
