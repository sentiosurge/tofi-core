package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ExecuteHTTP 是一个通用的 HTTP 请求执行器
func ExecuteHTTP(method, targetURL string, headers map[string]string, queryParams map[string]string, bodyStr string, timeout int) (string, error) {
	// 1. 处理超时
	if timeout <= 0 {
		timeout = 30
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}

	// 2. 处理 Query Params
	if len(queryParams) > 0 {
		u, err := url.Parse(targetURL)
		if err != nil {
			return "", fmt.Errorf("无效的 URL: %v", err)
		}
		q := u.Query()
		for k, v := range queryParams {
			q.Add(k, v)
		}
		u.RawQuery = q.Encode()
		targetURL = u.String()
	}

	// 3. 构造请求体
	var bodyReader io.Reader
	if bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequest(method, targetURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}

	// 4. 设置 Headers
	// 默认 Content-Type，如果用户没传，且有 body，默认为 JSON
	hasContentType := false
	for k := range headers {
		if http.CanonicalHeaderKey(k) == "Content-Type" {
			hasContentType = true
			break
		}
	}
	if bodyStr != "" && !hasContentType {
		req.Header.Set("Content-Type", "application/json")
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 5. 执行请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求执行失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	// 6. 状态码检查 (允许 2xx)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return string(respBody), fmt.Errorf("HTTP Error %d: %s", resp.StatusCode, string(respBody))
	}

	return string(respBody), nil
}

// StreamCallback is called for each SSE data chunk from a streaming response
type StreamCallback func(chunk string) error

// PostJSONStream sends a POST request and reads the response as an SSE stream.
// Each "data: {...}" line is passed to the callback. Stops on "data: [DONE]".
func PostJSONStream(targetURL string, headers map[string]string, payload interface{}, timeout int, callback StreamCallback) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if timeout <= 0 {
		timeout = 120
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}

	req, err := http.NewRequest("POST", targetURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("create request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP Error %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for large chunks
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
		if err := callback(data); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// PostJSON 发送通用的 OpenAI 兼容请求 (保留给 AI 任务使用)
func PostJSON(url string, headers map[string]string, payload interface{}, timeout int) (string, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	// 复用 ExecuteHTTP
	// 注意：PostJSON 默认会加上 Content-Type: application/json (ExecuteHTTP 已处理)
	return ExecuteHTTP("POST", url, headers, nil, string(jsonData), timeout)
}
