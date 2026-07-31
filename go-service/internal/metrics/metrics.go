package metrics

import (
	"context"

	"github.com/moddengine/whmcs-container-controller/internal/model"
)

type Provider interface {
	GetServiceMetrics(context.Context, string) (model.Metrics, error)
}

type Mock struct{}

func (Mock) GetServiceMetrics(context.Context, string) (model.Metrics, error) {
	return model.Metrics{Source: "mock", Available: true}, nil
}
