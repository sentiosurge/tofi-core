package models

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// ResolveLocalContext 执行第一阶段解析：Global -> Local Context
// 它遍历 Node.Input，将全局变量引用（{{node.id}}）替换为具体值
// 并根据定义的 ID 存入局部作用域 Map
func ResolveLocalContext(node *Node, ctx *ExecutionContext) (map[string]interface{}, error) {
	localContext := make(map[string]interface{})

	for _, param := range node.Input {
		if param.Var != nil {
			v := param.Var
			// 1. 解析 Value (支持全局引用和环境变量)
			resolved, err := resolveValueRecursive(v.Value, ctx)
			if err != nil && !v.Optional {
				return nil, fmt.Errorf("input.var '%s' resolution failed: %v", v.ID, err)
			}
			localContext[v.ID] = resolved
		} else if param.Secret != nil {
			s := param.Secret
			// 2. 解析 Secret (从环境变量或 Secret Store 获取)
			val := os.Getenv(s.Value)
			if val == "" && !s.Optional {
				return nil, fmt.Errorf("input.secret '%s' (key: %s) not found in environment", s.ID, s.Value)
			}
			localContext[s.ID] = val
			// 将敏感词加入脱敏列表
			ctx.AddSecretValue(val)
		}
	}

	return localContext, nil
}

// ResolveConfig 执行第二阶段解析：Local Context -> Config
// 它遍历 Node.Config，先尝试替换 LocalContext 变量，如果未解析完，再尝试替换 Global Context (ExecutionContext)
func ResolveConfig(config map[string]interface{}, localContext map[string]interface{}, ctx *ExecutionContext) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	for k, v := range config {
		resolved, err := resolveLocalTemplate(v, localContext, ctx)
		if err != nil {
			return nil, fmt.Errorf("config field '%s' resolution failed: %v", k, err)
		}
		result[k] = resolved
	}

	return result, nil
}

// resolveValueRecursive 全局解析逻辑 (用于 Input.Value)
func resolveValueRecursive(val interface{}, ctx *ExecutionContext) (interface{}, error) {
	switch v := val.(type) {
	case string:
		// 展开环境变量 ${VAR}
		expanded := expandEnvVars(v)
		// 替换全局变量 {{node_id}}
		replaced, err := ctx.ReplaceParamsStrict(expanded)
		if err != nil {
			return nil, err
		}
		return replaced, nil
	case map[string]interface{}:
		res := make(map[string]interface{})
		for k, sub := range v {
			r, err := resolveValueRecursive(sub, ctx)
			if err != nil {
				return nil, err
			}
			res[k] = r
		}
		return res, nil
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, sub := range v {
			r, err := resolveValueRecursive(sub, ctx)
			if err != nil {
				return nil, err
			}
			res[i] = r
		}
		return res, nil
	default:
		return v, nil
	}
}

// resolveLocalTemplate 局部解析逻辑 (用于 Config 引用 Input)
func resolveLocalTemplate(val interface{}, localContext map[string]interface{}, ctx *ExecutionContext) (interface{}, error) {
	switch v := val.(type) {
	case string:
		return resolveLocalString(v, localContext, ctx)
	case map[string]interface{}:
		res := make(map[string]interface{})
		for k, sub := range v {
			r, err := resolveLocalTemplate(sub, localContext, ctx)
			if err != nil {
				return nil, err
			}
			res[k] = r
		}
		return res, nil
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, sub := range v {
			r, err := resolveLocalTemplate(sub, localContext, ctx)
			if err != nil {
				return nil, err
			}
			res[i] = r
		}
		return res, nil
	default:
		return v, nil
	}
}

func resolveLocalString(s string, localContext map[string]interface{}, ctx *ExecutionContext) (interface{}, error) {
	// 如果不包含模版语法，直接返回
	if !strings.Contains(s, "{{") {
		return s, nil
	}

	// 处理转义字符：将 \{{ 临时替换为特殊标记
	const escapedPlaceholder = "__ESCAPED_BRACES__"
	result := strings.ReplaceAll(s, "\\{{", escapedPlaceholder)

	// 1. 局部变量优先替换
	for id, value := range localContext {
		// 1.1 基础替换 {{id}}
		placeholder := "{{" + id + "}}"
		if strings.Contains(result, placeholder) {
			// 如果值是复杂类型且整个字符串就是一个占位符，直接返回该对象（保持类型）
			if result == placeholder {
				return value, nil
			}
			result = strings.ReplaceAll(result, placeholder, fmt.Sprint(value))
		}

		// 1.2 JSON 路径替换 {{id.path}}
		prefix := "{{" + id + "."
		if strings.Contains(result, prefix) {
			// 如果 value 是对象，使用 gjson 提取
			jsonStr, _ := json.Marshal(value)
			for strings.Contains(result, prefix) {
				startIdx := strings.Index(result, prefix)
				endIdx := strings.Index(result[startIdx:], "}}") + startIdx
				fullPath := result[startIdx+2 : endIdx]
				jsonPath := strings.TrimPrefix(fullPath, id+".")

				gv := gjson.Get(string(jsonStr), jsonPath)
				if !gv.Exists() {
					// 局部变量里有这个前缀，但路径不对，可能是用户写错了
					// 但也有可能这个前缀恰好也是一个全局变量名？(概率较低，这里倾向于报错或忽略)
					// 为了安全，如果匹配到局部变量ID，应视为局部变量解析失败
					return nil, fmt.Errorf("local variable '%s' does not have path '%s'", id, jsonPath)
				}

				// 如果整个字符串就是这个路径，保持其原始类型
				if result == "{{"+fullPath+"}}" {
					return gv.Value(), nil
				}
				result = strings.ReplaceAll(result, "{{"+fullPath+"}}", gv.String())
			}
		}
	}

	// 2. 如果还有未解析的 {{}}，尝试全局替换
	// 注意：我们必须先处理完局部变量，再处理全局，以支持局部变量遮盖全局变量（Shadowing）
	if strings.Contains(result, "{{") && ctx != nil {
		var err error
		result, err = ctx.ReplaceParamsStrict(result)
		if err != nil {
			return nil, fmt.Errorf("resolution failed: %v", err)
		}
	}

	// 检查是否还有未解析的 {{}}
	if strings.Contains(result, "{{") {
		return nil, fmt.Errorf("unresolved variable in template: %s", result)
	}

	// 恢复转义字符
	result = strings.ReplaceAll(result, escapedPlaceholder, "{{")

	return result, nil
}

// expandEnvVars 展开环境变量 ${VAR_NAME}
func expandEnvVars(s string) string {
	re := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match
	})
}
