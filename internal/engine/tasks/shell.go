package tasks

import (
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
)

type Shell struct{}

func (s *Shell) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	script := ctx.ReplaceParams(n.Config["script"])
	return executor.ExecuteShell(script, n.Timeout)
}
