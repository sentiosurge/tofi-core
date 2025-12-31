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
		// 尝试隐式解析 (Global Context Only)
		if !strings.Contains(v, "{{") {
			if val, found := tryResolveImplicit(v, nil, ctx); found {
				return val, nil
			}
		}

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

	// 如果不包含模版语法，尝试进行"隐式引用解析" (Implicit Reference Resolution)

	// 即：如果字符串本身就是一个有效的变量名或路径 (如 "node_id" 或 "node_id.field")

	// 且能解析出有效值，则直接返回该值。

	if !strings.Contains(s, "{{") {

		if val, found := tryResolveImplicit(s, localContext, ctx); found {

			return val, nil

		}

		return s, nil

	}



	// ... (Existing Template Logic) ...

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

					// 但也有可能这个前缀恰好也是一个全局变量名？(概率较低，这里 倾向于报错或忽略)

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



// tryResolveImplicit 尝试解析不带 {{}} 的字符串引用

func tryResolveImplicit(s string, localContext map[string]interface{}, ctx *ExecutionContext) (interface{}, bool) {

	// 1. 尝试 Local Context (Full Match)

	if val, ok := localContext[s]; ok {

		return val, true

	}



	// 2. 尝试 Local Context (Path Match)

	// 遍历所有 local key，看 s 是否以 key + "." 开头

	for key, val := range localContext {

		prefix := key + "."

		if strings.HasPrefix(s, prefix) {

			jsonPath := strings.TrimPrefix(s, prefix)

			jsonStr, _ := json.Marshal(val)

			gv := gjson.Get(string(jsonStr), jsonPath)

			if gv.Exists() {

				return gv.Value(), true

			}

		}

	}



	// 3. 尝试 Global Context

	if ctx == nil {

		return nil, false

	}



	// 3.1 Global Full Match (Node ID)

	if val, ok := ctx.GetResult(s); ok {

		return val, true

	}



	// 3.2 Global Path Match

	// 由于 Node ID 可能包含点 (gpt.write)，我们不能简单分割第一个点

	// 需要尝试所有可能的分割点 (Longest Prefix Match preferred? Or just any valid node match)

	// 这里的策略是：从最长可能得 Node ID 开始匹配

	

	// 简单的暴力匹配：将 s 在每一个点处拆分，左边是 ID，右边是 Path

	// 为了优先匹配更长的 ID (如 gpt.write.essay vs gpt.write)，我们应该怎么做？

	// 只要找到一个存在的 Node ID，并且 Path 有效，就返回。

	// 如果有多个匹配？通常 Node ID 不会是另一个 Node ID 的前缀+点 (除非用户命名非常混乱)

	

	parts := strings.Split(s, ".")

	for i := len(parts) - 1; i > 0; i-- {

		nodeID := strings.Join(parts[:i], ".")

		jsonPath := strings.Join(parts[i:], ".")

		

		if valStr, ok := ctx.GetResult(nodeID); ok {

			// Node 存在，尝试获取 Path

			gv := gjson.Get(valStr, jsonPath)

			if gv.Exists() {

				return gv.Value(), true

			}

		}

	}



	return nil, false

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
