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

// openaiLegacy implements Provider using the OpenAI Chat Completions API.
// Used for OpenAI-compatible providers (Ollama, Groq, OpenRouter, DeepSeek, etc.).
type openaiLegacy struct {
	apiKey  string
	baseURL string
}

func newOpenAILegacy(apiKey string, cfg *providerConfig) (Provider, error) {
	baseURL := "https://api.openai.com/v1"
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	// API key may be empty for local providers like Ollama
	return &openaiLegacy{apiKey: apiKey, baseURL: baseURL}, nil
}

// Chat sends a non-streaming Chat Completions request.
func (o *openaiLegacy) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	payload := o.buildPayload(req, false)
	body, err := o.doRequest(ctx, payload)
	if err != nil {
		return nil, err
	}
	return o.parseResponse(body)
}

// ChatStream sends a streaming Chat Completions request.
func (o *openaiLegacy) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error) {
	payload := o.buildPayload(req, true)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

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

	return o.parseStream(resp.Body, onDelta)
}

// buildPayload constructs a Chat Completions request.
func (o *openaiLegacy) buildPayload(req *ChatRequest, stream bool) map[string]interface{} {
	// Build messages array
	var messages []map[string]interface{}

	// System message
	if req.System != "" {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": req.System,
		})
	}

	// Conversation messages
	for _, msg := range req.Messages {
		m := map[string]interface{}{
			"role": msg.Role,
		}

		switch msg.Role {
		case "assistant":
			if msg.Content != "" {
				m["content"] = msg.Content
			}
			if len(msg.ToolCalls) > 0 {
				var tcs []map[string]interface{}
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, map[string]interface{}{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      tc.Name,
							"arguments": tc.Arguments,
						},
					})
				}
				m["tool_calls"] = tcs
				// OpenAI requires content to be null or string when tool_calls present
				if msg.Content == "" {
					m["content"] = nil
				}
			}

		case "tool":
			m["content"] = msg.Content
			m["tool_call_id"] = msg.ToolCallID
			m["name"] = msg.ToolName

		default: // "user", "system"
			m["content"] = msg.Content
		}

		messages = append(messages, m)
	}

	payload := map[string]interface{}{
		"model":    req.Model,
		"messages": messages,
	}

	if stream {
		payload["stream"] = true
		payload["stream_options"] = map[string]interface{}{
			"include_usage": true,
		}
	}

	// Tools (Chat Completions format with function wrapper)
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			// Ensure parameters has required fields
			params := t.Parameters
			if params == nil {
				params = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  params,
				},
			})
		}
		payload["tools"] = tools
		payload["parallel_tool_calls"] = false
	}

	return payload
}

// doRequest sends a non-streaming POST.
func (o *openaiLegacy) doRequest(ctx context.Context, payload map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", strings.NewReader(string(jsonData)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

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

// parseResponse parses a Chat Completions response.
func (o *openaiLegacy) parseResponse(body string) (*ChatResponse, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{
		Usage: Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	if len(resp.Choices) > 0 {
		msg := resp.Choices[0].Message
		result.Content = msg.Content
		result.Reasoning = msg.ReasoningContent

		for _, tc := range msg.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}

	return result, nil
}

// parseStream parses Chat Completions streaming response.
func (o *openaiLegacy) parseStream(body io.Reader, onDelta func(StreamDelta)) (*ChatResponse, error) {
	result := &ChatResponse{}
	var contentBuf strings.Builder
	var reasoningBuf strings.Builder

	// Tool call accumulation
	type tcAccum struct {
		ID   string
		Name string
		Args strings.Builder
	}
	tcMap := make(map[int]*tcAccum)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta

			// Content
			if delta.Content != "" {
				contentBuf.WriteString(delta.Content)
				if onDelta != nil {
					onDelta(StreamDelta{Content: delta.Content})
				}
			}

			// Reasoning (DeepSeek)
			if delta.ReasoningContent != "" {
				reasoningBuf.WriteString(delta.ReasoningContent)
				if onDelta != nil {
					onDelta(StreamDelta{Reasoning: delta.ReasoningContent})
				}
			}

			// Tool calls
			for _, tc := range delta.ToolCalls {
				acc, ok := tcMap[tc.Index]
				if !ok {
					acc = &tcAccum{}
					tcMap[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.Args.WriteString(tc.Function.Arguments)
				}

				if onDelta != nil {
					onDelta(StreamDelta{
						ToolCalls: []ToolCallDelta{{
							Index:     tc.Index,
							ID:        tc.ID,
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						}},
					})
				}
			}
		}

		// Usage (last chunk)
		if chunk.Usage != nil {
			result.Usage.InputTokens = chunk.Usage.PromptTokens
			result.Usage.OutputTokens = chunk.Usage.CompletionTokens
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = contentBuf.String()
	result.Reasoning = reasoningBuf.String()

	// Assemble tool calls
	for i := 0; i < len(tcMap); i++ {
		if acc, ok := tcMap[i]; ok {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        acc.ID,
				Name:      acc.Name,
				Arguments: acc.Args.String(),
			})
		}
	}

	return result, nil
}
