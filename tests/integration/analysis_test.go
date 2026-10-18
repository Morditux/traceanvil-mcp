package integration

import (
	"context"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

func newAnalysisService(t *testing.T, h *memcheckHarness, timeout time.Duration) *analysis.Service {
	t.Helper()
	service, err := analysis.New(analysis.Options{
		Store:          h.store,
		Backend:        analysis.NewValgrindBackend(h.service),
		BackendEnabled: true,
		Timeout:        timeout,
	})
	if err != nil {
		t.Fatalf("analysis.New() error = %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func waitForTerminalPhase(t *testing.T, service *analysis.Service, runID string) analysis.Status {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, err := service.Status(context.Background(), analysis.StatusRequest{RunID: runID})
		if err != nil {
			t.Fatalf("Status(%s) error = %v", runID, err)
		}
		switch status.Phase {
		case analysis.PhaseCompleted, analysis.PhaseFailed, analysis.PhaseCancelled, analysis.PhaseTimedOut:
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach a terminal phase: %+v", runID, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAnalysisCancelStopsBlockedProgram(t *testing.T) {
	h := newMemcheckHarness(t, "hang", 30*time.Second, 0)
	service := newAnalysisService(t, h, 30*time.Second)
	ctx := context.Background()

	started, err := service.Start(ctx, analysis.StartRequest{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	canceled, err := service.Cancel(ctx, analysis.CancelRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !canceled.CancelRequested {
		t.Fatalf("cancel_requested = false: %+v", canceled)
	}

	final := waitForTerminalPhase(t, service, started.RunID)
	if final.State != analysis.StateCancelled {
		t.Errorf("state = %q, want cancelled (status: %+v)", final.State, final)
	}
	if final.Phase != analysis.PhaseCancelled {
		t.Errorf("phase = %q, want cancelled", final.Phase)
	}
	if final.AnalysisComplete {
		t.Error("analysis_complete = true after cancellation")
	}
	if final.Result == diagnostic.ResultNoErrorsObserved {
		t.Errorf("result = no_errors_observed after cancellation")
	}

	deadline := time.Now().Add(10 * time.Second)
	for h.manager.Active() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("job manager still reports active jobs after cancellation")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAnalysisPerRequestTimeout(t *testing.T) {
	h := newMemcheckHarness(t, "hang", 30*time.Second, 0)
	service := newAnalysisService(t, h, 30*time.Second)
	ctx := context.Background()

	started, err := service.Start(ctx, analysis.StartRequest{
		TargetID:  "fixture",
		TimeoutMS: 300,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	final := waitForTerminalPhase(t, service, started.RunID)
	if final.State != analysis.StateTimedOut || final.Phase != analysis.PhaseTimedOut {
		t.Errorf("state/phase = %q/%q, want timed_out/timed_out", final.State, final.Phase)
	}
	if final.AnalysisComplete {
		t.Error("analysis_complete = true after timeout")
	}
	if final.Result == diagnostic.ResultNoErrorsObserved {
		t.Error("result = no_errors_observed after timeout")
	}
}

func TestAnalysisTimeoutAboveMaximumRejected(t *testing.T) {
	h := newMemcheckHarness(t, "clean", 30*time.Second, 0)
	service := newAnalysisService(t, h, 2*time.Second)

	_, err := service.Start(context.Background(), analysis.StartRequest{
		TargetID:  "fixture",
		TimeoutMS: 2001,
	})
	if err == nil {
		t.Fatal("Start() error = nil, want invalid_argument")
	}
	if code := analysis.CodeOf(err); code != analysis.CodeInvalidArgument {
		t.Errorf("error code = %q, want invalid_argument (%v)", code, err)
	}
}

func TestAnalysisDiffRealRuns(t *testing.T) {
	h := newMemcheckHarness(t, "invalid_read", 30*time.Second, 0)
	service := newAnalysisService(t, h, 30*time.Second)
	ctx := context.Background()

	run := func() string {
		started, err := service.Start(ctx, analysis.StartRequest{TargetID: "fixture"})
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		final := waitForTerminalPhase(t, service, started.RunID)
		if final.State != analysis.StateCompleted || final.Phase != analysis.PhaseCompleted {
			t.Fatalf("final = %+v", final)
		}
		return started.RunID
	}
	baseID := run()
	targetID := run()
	result, err := service.Diff(ctx, analysis.DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if result.Compatibility.FingerprintAlgorithm != diagnostic.FingerprintAlgorithmV2 {
		t.Errorf("algorithm = %q", result.Compatibility.FingerprintAlgorithm)
	}
	if result.Summary.Persistent != 1 || result.Summary.New != 0 || result.Summary.Resolved != 0 {
		t.Errorf("summary = %+v", result.Summary)
	}
	if len(result.Items) != 1 || result.Items[0].Change != diagnostic.ChangePersistent {
		t.Errorf("items = %+v", result.Items)
	}
}
