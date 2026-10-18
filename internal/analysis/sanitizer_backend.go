package analysis

import (
	"context"

	"github.com/Morditux/traceanvil-mcp/internal/sanitizer"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

// SanitizerBackend adapts *sanitizer.Service to the analysis Backend interface.
type SanitizerBackend struct {
	Service *sanitizer.Service
}

// NewSanitizerBackend returns an adapter for a sanitizer service.
func NewSanitizerBackend(service *sanitizer.Service) *SanitizerBackend {
	return &SanitizerBackend{Service: service}
}

// Start implements Backend.
func (b *SanitizerBackend) Start(ctx context.Context, req valgrind.Request) (Run, error) {
	if b == nil || b.Service == nil {
		return nil, &sanitizer.BackendError{Reason: "sanitizer service is not configured"}
	}
	run, err := b.Service.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// Get implements Backend.
func (b *SanitizerBackend) Get(runID string) (Run, bool) {
	if b == nil || b.Service == nil {
		return nil, false
	}
	run, ok := b.Service.Get(runID)
	if !ok || run == nil {
		return nil, false
	}
	return run, true
}
