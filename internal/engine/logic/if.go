package logic

import (
	"fmt"
	"strings" // 👈 引入 strings 包
	"tofi-core/internal/models"

	"github.com/Knetic/govaluate"
)

type If struct{}

func (i *If) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	exprStr := ctx.ReplaceParams(n.Config["if"])

	// --- 新增：定义自定义函数库 ---
	functions := map[string]govaluate.ExpressionFunction{
		"contains": func(args ...interface{}) (interface{}, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("'contains' requires 2 arguments")
			}
			// 检查参数是否为字符串，并执行判定
			return strings.Contains(fmt.Sprint(args[0]), fmt.Sprint(args[1])), nil
		},
		// 以后你可以在这里加更多函数，比如 length, toUpper 等
	}

	// 解析表达式时传入 functions
	expression, err := govaluate.NewEvaluableExpressionWithFunctions(exprStr, functions)
	if err != nil {
		return exprStr, fmt.Errorf("表达式解析失败: %v", err)
	}

	result, err := expression.Evaluate(nil)
	if err != nil {
		return exprStr, fmt.Errorf("逻辑判定执行失败: %v", err)
	}

	if isPassed, ok := result.(bool); !ok || !isPassed {
		return exprStr, fmt.Errorf("CONDITION_NOT_MET")
	}

	return exprStr, nil
}
