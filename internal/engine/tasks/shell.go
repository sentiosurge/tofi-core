package tasks

import (
	"fmt"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
)

type Shell struct{}

func (s *Shell) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	script := fmt.Sprint(config["script"])
	if script == "" {
		return "", fmt.Errorf("shell script is required")
	}

	// Env
	env := make(map[string]string)

	// 注入魔术环境变量 (Magic Env Vars)
	env["TOFI_ARTIFACTS_DIR"] = ctx.Paths.Artifacts
	env["TOFI_UPLOADS_DIR"] = ctx.Paths.Uploads
	env["TOFI_EXECUTION_ID"] = ctx.ExecutionID

	if rawEnv := config["env"]; rawEnv != nil {
		if m, ok := rawEnv.(map[string]interface{}); ok {
			for k, v := range m {
				env[k] = fmt.Sprint(v)
			}
		}
	}

	// 注意：在之前的版本中，Shell 节点禁止了 {{}} 模板语法，强制通过 env 注入。
	// 在新规范下，这个约束依然有效，因为 Config 里的 script 应该已经是解析过的了。
	// 但通常 Shell 的 script 我们建议在 Config 里直接写死，变量放在 Input/Env 中。

	return executor.ExecuteShell(script, env, 60)
}

func (s *Shell) Validate(n *models.Node) error {
	if _, ok := n.Config["script"]; !ok {
		return fmt.Errorf("config.script is required")
	}
	return nil
}
