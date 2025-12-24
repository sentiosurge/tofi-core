package base

import "tofi-core/internal/models"

type Virtual struct{}

func (v *Virtual) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	return "VIRTUAL_OK", nil
}

func (v *Virtual) Validate(n *models.Node) error {
	return nil
}
