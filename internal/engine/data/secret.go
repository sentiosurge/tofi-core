package data

import (
	"encoding/json"
	"fmt"
	"os"
	"tofi-core/internal/models"
)

type Secret struct{}

func (s *Secret) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// Secret 节点现在作为一个"密钥字典"运行
	// Config 中的 Key 是内部使用的名称，Value 是环境变量的名称
	results := make(map[string]string)

	for k, v := range config {
		envVarName, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("secret node config values must be strings (environment variable names), got %T for key '%s'", v, k)
		}

		// 1. 从环境变量获取实际值
		realValue := os.Getenv(envVarName)
		
		// 2. 如果环境变量为空，且它看起来不像是一个 ENV_VAR_NAME (比如是空字符串)，可以选择报错或者允许为空
		// 这里我们选择：如果找不到，就是空字符串，但最好打印个警告或报错，取决于是否 strict。
		// 目前策略：允许为空，但在日志中会被 mask 为空
		
		results[k] = realValue

		// 3. 注册到全局 Context 进行脱敏 (Masking)
		// 只有非空值才注册，否则会把所有空字符串都替换成 ******，导致日志乱码
		if realValue != "" {
			ctx.AddSecretValue(realValue)
		}
	}

	// 4. 序列化为 JSON 返回，以便下游节点通过 {{secrets.key}} 引用
	bytes, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("failed to marshal secrets: %v", err)
	}

	return string(bytes), nil
}

func (s *Secret) Validate(n *models.Node) error {
	if len(n.Config) == 0 {
		return fmt.Errorf("secret node requires config (key: env_var_name)")
	}
	return nil
}
