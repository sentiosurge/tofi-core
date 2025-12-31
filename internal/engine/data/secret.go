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

	results := make(map[string]string)



	for k, v := range config {

		strVal, ok := v.(string)

		if !ok {

			return "", fmt.Errorf("secret node config values must be strings, got %T for key '%s'", v, k)

		}



		var realValue string



		// 解析 Value 格式
		// 1. {{env.VAR_NAME}}
		// 2. Literal String (Direct Value)
		if len(strVal) > 7 && strVal[:6] == "{{env." && strVal[len(strVal)-2:] == "}}" {
			envVarName := strVal[6 : len(strVal)-2]
			realValue = os.Getenv(envVarName)
		} else {
			realValue = strVal
		}



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
