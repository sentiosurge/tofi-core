package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// RegistryClient 连接 skills.sh 或其他 Agent Skills Registry
type RegistryClient struct {
	baseURL    string
	httpClient *http.Client
}

// RegistrySkill 代表 registry 中的一个技能概要
type RegistrySkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	RepoURL     string `json:"repo_url"`
	Stars       int    `json:"stars"`
	Downloads   int    `json:"downloads"`
	Tags        []string `json:"tags"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// RegistrySearchResult 搜索结果
type RegistrySearchResult struct {
	Skills []RegistrySkill `json:"skills"`
	Total  int             `json:"total"`
	Page   int             `json:"page"`
}

// NewRegistryClient 创建 registry 客户端
// 默认使用 skills.sh，也支持自定义 registry
func NewRegistryClient(baseURL string) *RegistryClient {
	if baseURL == "" {
		baseURL = "https://skills.sh/api"
	}
	return &RegistryClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Search 搜索 registry 中的技能
func (c *RegistryClient) Search(query string, page, limit int) (*RegistrySearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if page <= 0 {
		page = 1
	}

	u := fmt.Sprintf("%s/skills/search?q=%s&page=%d&limit=%d",
		c.baseURL, url.QueryEscape(query), page, limit)

	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("registry search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, string(body))
	}

	var result RegistrySearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse registry response: %w", err)
	}

	return &result, nil
}

// Trending 获取热门技能
func (c *RegistryClient) Trending(limit int) (*RegistrySearchResult, error) {
	if limit <= 0 {
		limit = 20
	}

	u := fmt.Sprintf("%s/skills/trending?limit=%d", c.baseURL, limit)

	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("registry trending failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, string(body))
	}

	var result RegistrySearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse registry response: %w", err)
	}

	return &result, nil
}

// GetSkillContent 获取技能的 SKILL.md 内容（如果 registry 支持直接获取）
func (c *RegistryClient) GetSkillContent(name string) (string, error) {
	u := fmt.Sprintf("%s/skills/%s/content", c.baseURL, url.PathEscape(name))

	resp, err := c.httpClient.Get(u)
	if err != nil {
		return "", fmt.Errorf("failed to get skill content: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("skill '%s' not found in registry (status %d)", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}
