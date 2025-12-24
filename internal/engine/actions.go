package engine

import "tofi-core/internal/models"

type Action interface {
	Execute(node *models.Node, ctx *models.ExecutionContext) (string, error)
	Validate(node *models.Node) error
}
