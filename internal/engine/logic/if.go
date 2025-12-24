package logic

import (
	"fmt"
	"strings"
	"tofi-core/internal/models"

	"github.com/Knetic/govaluate"
)

type If struct{}

func (i *If) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// 1. 【关键】直接获取原始表达式，绝对不要在这里调用 ctx.ReplaceParams
	// 这样表达式里就不会被塞入几千字的 Markdown，保持公式干净
	exprStr := n.Config["if"]

	// 2. 注入常用函数库
	functions := map[string]govaluate.ExpressionFunction{
		"contains": func(args ...interface{}) (interface{}, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("contains 需要2个参数")
			}
			return strings.Contains(fmt.Sprint(args[0]), fmt.Sprint(args[1])), nil
		},
		"len": func(args ...interface{}) (interface{}, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("len 需要1个参数")
			}
			return float64(len(fmt.Sprint(args[0]))), nil
		},
	}

	// 3. 【核心修复】参数化注入
	// 既然表达式里写的是变量名（如 ai_review），我们就把结果集传进去
	// 解析器会自动去找 ai_review 对应的内容，不再有引号冲突
	parameters := make(map[string]interface{})
	for k, v := range ctx.Results {
		parameters[k] = v
	}

	// 4. 解析与安全计算
	expression, err := govaluate.NewEvaluableExpressionWithFunctions(exprStr, functions)
	if err != nil {
		return exprStr, fmt.Errorf("表达式解析失败 (语法错误): %v", err)
	}

	result, err := expression.Evaluate(parameters) // 👈 变量在这里静默注入
	if err != nil {
		return exprStr, fmt.Errorf("逻辑判定失败 (变量引用错误): %v", err)
	}

	if isPassed, ok := result.(bool); !ok || !isPassed {
		return exprStr, fmt.Errorf("CONDITION_NOT_MET")
	}

	return "EXPR_MATCHED", nil
}
