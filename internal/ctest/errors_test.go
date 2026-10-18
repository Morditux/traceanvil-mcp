package ctest

import (
	"errors"
	"io"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
)

func TestErrorHelpers(t *testing.T) {
	base := errors.New("base")
	wrapped := Wrap(CodeTimeout, base, "timed out")
	if CodeOf(wrapped) != CodeTimeout || !errors.Is(wrapped, base) {
		t.Fatalf("wrapped = %v", wrapped)
	}
	if wrapped.Error() == "" || Errorf(CodeInternal, "plain").Error() == "" {
		t.Fatal("empty error message")
	}
	if CodeOf(io.EOF) != CodeInternal {
		t.Fatal("plain error code")
	}
}

func TestMapStartError(t *testing.T) {
	if CodeOf(mapStartError(&jobs.LimitError{Max: 1})) != CodeQuotaExceeded {
		t.Error("job limit was not mapped to quota_exceeded")
	}
	if CodeOf(mapStartError(artifacts.ErrQuota)) != CodeQuotaExceeded {
		t.Error("artifact quota was not mapped")
	}
	if CodeOf(mapStartError(jobs.ErrClosed)) != CodeClosed {
		t.Error("closed manager was not mapped")
	}
	if CodeOf(mapStartError(io.EOF)) != CodeInternal {
		t.Error("unknown error was not mapped to internal")
	}
}
