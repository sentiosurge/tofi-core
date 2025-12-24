package tasks

import (
	"fmt"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
)

type Shell struct{}

func (s *Shell) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	rawScript := n.Input["script"]

	// 🔒 安全强校验：禁止在 Script 中直接使用模版语法
	// 强制用户通过 Env 注入变量，防止 Shell 注入攻击
	if containsTemplateSyntax(rawScript) {
		return "", fmt.Errorf("SECURITY_VIOLATION: 直接在 Shell 脚本中使用 '{{...}}' 是禁止的。请使用 'env' 字段传递变量，并在脚本中通过 \"$VAR\" 引用。")
	}

	// 既然禁止了 {{}}，直接使用原始内容
	script := rawScript

	// 处理 Env 变量替换
	finalEnv := make(map[string]string)
	for k, v := range n.Env {
		finalEnv[k] = ctx.ReplaceParams(v)
	}

	return executor.ExecuteShell(script, finalEnv, n.Timeout)
}

func containsTemplateSyntax(s string) bool {
	return strings.Contains(s, "{{")
}