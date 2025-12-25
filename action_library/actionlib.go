package actionlib

import (
	"embed"
	"fmt"
)

//go:embed *.yaml
var ActionLibraryFS embed.FS

// ReadAction 读取指定名称的 action YAML 文件
func ReadAction(actionName string) ([]byte, error) {
	embedPath := actionName + ".yaml"
	data, err := ActionLibraryFS.ReadFile(embedPath)
	if err != nil {
		return nil, fmt.Errorf("官方 action 不存在: %s", actionName)
	}
	return data, nil
}

// Exists 检查指定的 action 是否存在
func Exists(actionName string) bool {
	embedPath := actionName + ".yaml"
	_, err := ActionLibraryFS.ReadFile(embedPath)
	return err == nil
}
