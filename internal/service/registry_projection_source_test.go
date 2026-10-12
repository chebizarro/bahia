package service

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
)

func TestRegistryProjectionSourceRejectsMissingRepository(t *testing.T) {
	registry := NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	source, err := registry.ProjectionSource()
	if source != nil {
		t.Fatal("partially wired registry was handed to projector")
	}
	var missing *MissingRegistryRepositoryError
	if !errors.As(err, &missing) || missing.Repository != "services" {
		t.Fatalf("error = %v, want typed missing-services error", err)
	}
	_, err = registry.ListServices(context.Background())
	if !errors.As(err, &missing) || missing.Repository != "services" {
		t.Fatalf("ListServices error = %v, want typed missing-services error", err)
	}
}
