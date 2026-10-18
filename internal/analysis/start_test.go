package analysis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func TestStartValidation(t *testing.T) {
	store := newTestStore(t)

	t.Run("disabled backend", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
			opts.BackendEnabled = false
		})
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture"})
		mustCode(t, err, CodeToolDisabled)
	})

	t.Run("unavailable backend", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
			opts.BackendAvailable = func() (bool, string) { return false, "valgrind not installed" }
		})
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture"})
		mustCode(t, err, CodeBackendUnavailable)
	})

	t.Run("unknown tool", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture", Tool: "callgrind"})
		mustCode(t, err, CodeUnknownTool)
	})

	t.Run("helgrind options rejected", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{
			TargetID: "fixture", Tool: ToolHelgrind,
			Options: &MemcheckOptions{LeakCheck: strPtr("full")},
		})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("empty target", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{TargetID: "  "})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("negative timeout", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture", TimeoutMS: -1})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("timeout above maximum", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
			opts.Timeout = 2 * time.Second
		})
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture", TimeoutMS: 2001})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("invalid num_callers", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		zero := 0
		_, err := service.Start(context.Background(), StartRequest{
			TargetID: "fixture",
			Options:  &MemcheckOptions{NumCallers: &zero},
		})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("empty leak check", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{
			TargetID: "fixture",
			Options:  &MemcheckOptions{LeakCheck: strPtr("")},
		})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("empty show leak kinds", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		_, err := service.Start(context.Background(), StartRequest{
			TargetID: "fixture",
			Options:  &MemcheckOptions{ShowLeakKinds: strPtr("")},
		})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("trace children rejected", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		enabled := true
		_, err := service.Start(context.Background(), StartRequest{
			TargetID: "fixture",
			Options:  &MemcheckOptions{TraceChildren: &enabled},
		})
		mustCode(t, err, CodeInvalidArgument)
	})

	t.Run("canceled context", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := service.Start(ctx, StartRequest{TargetID: "fixture"})
		mustCode(t, err, CodeCanceled)
	})

	t.Run("closed service", func(t *testing.T) {
		service := newTestService(t, store, newFakeBackend(), nil)
		service.Close()
		_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture"})
		mustCode(t, err, CodeClosed)
	})
}

func TestStartPassesValidatedRequest(t *testing.T) {
	store := newTestStore(t)
	backend := newFakeBackend()
	service := newTestService(t, store, backend, nil)

	status, err := service.Start(context.Background(), StartRequest{
		TargetID:  "fixture",
		Args:      []string{"--one"},
		TimeoutMS: 1500,
		Options: &MemcheckOptions{
			LeakCheck:     strPtr("summary"),
			TrackOrigins:  boolPtr(false),
			NumCallers:    intPtr(7),
			TraceChildren: boolPtr(false),
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if status.RunID == "" || status.SchemaVersion != APISchemaVersion {
		t.Errorf("status = %+v", status)
	}
	if status.Tool != ToolMemcheck || status.State != StateRunning || status.Phase != PhaseRunning {
		t.Errorf("status = %+v", status)
	}
	if len(status.Warnings) == 0 {
		t.Error("status warnings is empty, want a not-complete warning")
	}

	request := backend.lastRequest()
	if request.TargetID != "fixture" {
		t.Errorf("TargetID = %q", request.TargetID)
	}
	if len(request.Args) != 1 || request.Args[0] != "--one" {
		t.Errorf("Args = %v", request.Args)
	}
	if request.Timeout != 1500*time.Millisecond {
		t.Errorf("Timeout = %v, want 1.5s", request.Timeout)
	}
	if request.Options == nil {
		t.Fatal("Options = nil")
	}
	if request.Options.LeakCheck != valgrind.LeakCheckSummary {
		t.Errorf("LeakCheck = %q", request.Options.LeakCheck)
	}
	if request.Options.TrackOrigins {
		t.Error("TrackOrigins = true, want false")
	}
	if request.Options.NumCallers != 7 {
		t.Errorf("NumCallers = %d, want 7", request.Options.NumCallers)
	}
}

func TestStartDefaults(t *testing.T) {
	store := newTestStore(t)
	backend := newFakeBackend()
	service := newTestService(t, store, backend, nil)

	if _, err := service.Start(context.Background(), StartRequest{TargetID: "fixture"}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	request := backend.lastRequest()
	if request.Options != nil {
		t.Errorf("Options = %+v, want nil so the backend applies its defaults", request.Options)
	}
	if request.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (manager default)", request.Timeout)
	}
}

func TestStartErrorMapping(t *testing.T) {
	store := newTestStore(t)
	tests := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{name: "unknown target", err: &policy.DeniedError{TargetID: "x", Reason: "unknown target"}, want: CodeTargetUnknown},
		{name: "denied target", err: &policy.DeniedError{TargetID: "x", Reason: "program is not accessible"}, want: CodePolicyDenied},
		{name: "concurrency", err: &jobs.LimitError{Max: 1}, want: CodeQuotaExceeded},
		{name: "artifacts", err: artifacts.ErrQuota, want: CodeQuotaExceeded},
		{name: "backend", err: &valgrind.BackendError{Reason: "valgrind binary is missing"}, want: CodeBackendUnavailable},
		{name: "closed", err: valgrind.ErrClosed, want: CodeClosed},
		{name: "canceled", err: context.Canceled, want: CodeCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: CodeTimeout},
		{name: "option", err: &valgrind.OptionError{Field: "leak_check", Reason: "invalid"}, want: CodeInvalidArgument},
		{name: "unknown", err: errors.New("boom"), want: CodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeBackend()
			backend.startFn = func(context.Context, valgrind.Request) (Run, error) {
				return nil, test.err
			}
			service := newTestService(t, store, backend, nil)
			_, err := service.Start(context.Background(), StartRequest{TargetID: "fixture"})
			mustCode(t, err, test.want)
		})
	}
}

func strPtr(value string) *string { return &value }
func boolPtr(value bool) *bool    { return &value }
func intPtr(value int) *int       { return &value }

func TestStartHelgrindSelectsTool(t *testing.T) {
	store := newTestStore(t)
	backend := newFakeBackend()
	service := newTestService(t, store, backend, nil)
	status, err := service.Start(context.Background(), StartRequest{
		TargetID: "fixture",
		Tool:     ToolHelgrind,
		Args:     []string{"--once"},
	})
	if err != nil {
		t.Fatalf("Start(helgrind) error = %v", err)
	}
	if status.Tool != ToolHelgrind {
		t.Errorf("status tool = %q, want helgrind", status.Tool)
	}
	request := backend.lastRequest()
	if request.Tool != valgrind.ToolHelgrind {
		t.Errorf("backend tool = %q", request.Tool)
	}
	if request.Options != nil {
		t.Errorf("backend options = %+v, want nil for helgrind", request.Options)
	}
}
