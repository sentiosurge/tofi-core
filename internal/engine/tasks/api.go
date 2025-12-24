package tasks

import (
	"fmt"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
)

type API struct{}

func (a *API) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// 1. 解析基础配置
	method := strings.ToUpper(ctx.ReplaceParams(n.Config["method"]))
	url := ctx.ReplaceParams(n.Config["url"])
	auth := ctx.ReplaceParams(n.Config["api_key"])
	body := ctx.ReplaceParams(n.Config["body"])

	if method == "" {
		method = "POST"
	} // 默认 POST

	// 2. 构造 Headers
	headers := make(map[string]string)
	if auth != "" {
		headers["Authorization"] = "Bearer " + auth
	}

	// 3. 执行请求 (复用我们之前写的 executor.PostJSON)
	// 如果是 GET 请求，以后可以在 executor 里扩展
	resp, err := executor.PostJSON(url, headers, body, n.Timeout)
	if err != nil {
		return "", fmt.Errorf("API 请求失败: %v", err)
	}

	return resp, nil
}
