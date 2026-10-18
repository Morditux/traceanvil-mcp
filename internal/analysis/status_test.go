package analysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func TestStatusLiveAndCancel(t *testing.T) {
	store := newTestStore(t)
	backend := newFakeBackend()
	service := newTestService(t, store, backend, nil)
	ctx := context.Background()

	started, err := service.Start(ctx, StartRequest{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	runInterface, ok := backend.Get(started.RunID)
	if !ok {
		t.Fatalf("backend does not know run %q", started.RunID)
	}
	run := runInterface.(*fakeRun)

	running, err := service.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if running.State != StateRunning || running.Phase != PhaseRunning {
		t.Errorf("state/phase = %q/%q, want running/running", running.State, running.Phase)
	}
	if running.ReportAvailable {
		t.Error("report_available = true for a running analysis")
	}
	if running.Result != "" {
		t.Errorf("result = %q, want empty before completion", running.Result)
	}
	found := false
	for _, warning := range running.Warnings {
		if strings.Contains(warning, "not complete") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a not-complete warning", running.Warnings)
	}

	canceled, err := service.Cancel(ctx, CancelRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !canceled.CancelRequested {
		t.Error("cancel_requested = false for a running analysis")
	}
	if !run.canceled {
		t.Error("backend run was not canceled")
	}

	report := sampleReport(started.RunID, []diagnostic.Diagnostic{sampleDiagnostic(0)})
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeFilesAt(t, store, started.RunID, map[string][]byte{
		valgrind.AnalysisArtifactName: data,
	})
	finished := time.Now().UTC()
	result := &jobs.Result{
		Started:     true,
		ExitCode:    42,
		DurationMS:  250,
		StdoutBytes: 12,
		StderrBytes: 3,
	}
	run.set(
		valgrind.AnalysisSnapshot{RunID: started.RunID, Phase: valgrind.PhaseCompleted, Report: &report},
		jobs.Snapshot{
			SchemaVersion: jobs.SchemaVersion,
			RunID:         started.RunID,
			TargetID:      "fixture",
			State:         jobs.StateCompleted,
			CreatedAt:     finished.Add(-time.Second),
			StartedAt:     timePtr(finished.Add(-500 * time.Millisecond)),
			FinishedAt:    &finished,
			Result:        result,
		},
	)

	completed, err := service.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if completed.State != StateCompleted || completed.Phase != PhaseCompleted {
		t.Errorf("state/phase = %q/%q, want completed/completed", completed.State, completed.Phase)
	}
	if completed.Result != diagnostic.ResultErrorsFound || !completed.AnalysisComplete {
		t.Errorf("result = %q complete=%v", completed.Result, completed.AnalysisComplete)
	}
	if !completed.ReportAvailable {
		t.Error("report_available = false, want true")
	}
	if completed.Metrics.Diagnostics != 1 || completed.Metrics.Occurrences != 1 {
		t.Errorf("metrics = %+v", completed.Metrics)
	}
	if completed.Metrics.StdoutBytes != 12 || completed.Metrics.ReportBytes == 0 {
		t.Errorf("metrics = %+v, want job and report sizes", completed.Metrics)
	}
	if completed.Program == nil || completed.Program.ExitCode != 42 {
		t.Errorf("program = %+v, want the report program", completed.Program)
	}
	if completed.TargetID != "fixture" {
		t.Errorf("target_id = %q", completed.TargetID)
	}

	finishedCancel, err := service.Cancel(ctx, CancelRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if finishedCancel.CancelRequested {
		t.Error("cancel_requested = true for an already finished run")
	}
	if finishedCancel.State != StateCompleted {
		t.Errorf("state = %q, want completed", finishedCancel.State)
	}
}

func TestStatusFromDisk(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	started := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	runID := "run_00000000000000000000000000000011"
	manifest := jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion,
		RunID:         runID,
		TargetID:      "fixture",
		State:         jobs.StateCompleted,
		CreatedAt:     started,
		StartedAt:     &started,
		FinishedAt:    &finished,
		Result: &jobs.Result{
			Started:     true,
			ExitCode:    42,
			DurationMS:  2000,
			StdoutBytes: 5,
			StderrBytes: 6,
		},
	}
	report := sampleReport(runID, []diagnostic.Diagnostic{sampleDiagnostic(0)})
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	reportData, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeFilesAt(t, store, runID, map[string][]byte{
		jobs.ManifestName:             manifestData,
		valgrind.AnalysisArtifactName: reportData,
	})

	status, err := service.Status(ctx, StatusRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateCompleted || status.Phase != PhaseCompleted {
		t.Errorf("state/phase = %q/%q", status.State, status.Phase)
	}
	if status.Result != diagnostic.ResultErrorsFound || !status.AnalysisComplete {
		t.Errorf("result = %q complete=%v", status.Result, status.AnalysisComplete)
	}
	if !status.ReportAvailable || status.Metrics.ReportBytes == 0 {
		t.Errorf("report_available=%v report_bytes=%d", status.ReportAvailable, status.Metrics.ReportBytes)
	}
	if status.Progress.ElapsedMS != 2000 {
		t.Errorf("elapsed_ms = %d, want 2000", status.Progress.ElapsedMS)
	}
	if status.StartedAt != started.Format(time.RFC3339Nano) {
		t.Errorf("started_at = %q", status.StartedAt)
	}

	canceled, err := service.Cancel(ctx, CancelRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if canceled.CancelRequested {
		t.Error("cancel_requested = true for a persisted run")
	}
	if canceled.State != StateCompleted {
		t.Errorf("state = %q, want completed", canceled.State)
	}
}

func TestStatusFromDiskDegradedEvidence(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	started := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	manifest := jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion,
		RunID:         "run_00000000000000000000000000000021",
		TargetID:      "fixture",
		State:         jobs.StateCompleted,
		CreatedAt:     started,
		StartedAt:     &started,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	manifestOnly := "run_00000000000000000000000000000021"
	writeFilesAt(t, store, manifestOnly, map[string][]byte{jobs.ManifestName: manifestData})
	status, err := service.Status(ctx, StatusRequest{RunID: manifestOnly})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateCompleted || status.Phase != PhaseCompleted {
		t.Errorf("state/phase = %q/%q", status.State, status.Phase)
	}
	if status.ReportAvailable {
		t.Error("report_available = true without a report")
	}
	if status.Result != "" {
		t.Errorf("result = %q, want empty", status.Result)
	}

	reportOnly := "run_00000000000000000000000000000022"
	report := sampleReport(reportOnly, []diagnostic.Diagnostic{sampleDiagnostic(0)})
	reportData, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeFilesAt(t, store, reportOnly, map[string][]byte{
		valgrind.AnalysisArtifactName: reportData,
	})
	status, err = service.Status(ctx, StatusRequest{RunID: reportOnly})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateUnknown {
		t.Errorf("state = %q, want unknown without a manifest", status.State)
	}
	if status.Phase != PhaseCompleted {
		t.Errorf("phase = %q, want completed from the report", status.Phase)
	}
	if status.Result != diagnostic.ResultErrorsFound {
		t.Errorf("result = %q", status.Result)
	}

	emptyRun := "run_00000000000000000000000000000023"
	runDirPath(t, store, emptyRun)
	status, err = service.Status(ctx, StatusRequest{RunID: emptyRun})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateUnknown || status.Phase != PhaseUnknown {
		t.Errorf("state/phase = %q/%q, want unknown/unknown", status.State, status.Phase)
	}
	if status.Error == "" {
		t.Error("error is empty for a run without evidence")
	}

	invalidManifestRun := "run_00000000000000000000000000000024"
	writeFilesAt(t, store, invalidManifestRun, map[string][]byte{
		jobs.ManifestName: []byte("not json"),
	})
	status, err = service.Status(ctx, StatusRequest{RunID: invalidManifestRun})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateUnknown {
		t.Errorf("state = %q, want unknown for an unreadable manifest", status.State)
	}
	warned := false
	for _, warning := range status.Warnings {
		if strings.Contains(warning, "manifest is unreadable") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want an unreadable-manifest warning", status.Warnings)
	}
}

func TestStatusTimedOutFromDisk(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)

	runID := "run_00000000000000000000000000000031"
	manifest := jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion,
		RunID:         runID,
		TargetID:      "fixture",
		State:         jobs.StateTimedOut,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFilesAt(t, store, runID, map[string][]byte{jobs.ManifestName: manifestData})

	status, err := service.Status(context.Background(), StatusRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != StateTimedOut || status.Phase != PhaseTimedOut {
		t.Errorf("state/phase = %q/%q, want timed_out/timed_out", status.State, status.Phase)
	}
}

func TestStatusUnknownRun(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	_, err := service.Status(context.Background(), StatusRequest{RunID: "run_ffffffffffffffffffffffffffffffff"})
	mustCode(t, err, CodeRunNotFound)
	_, err = service.Cancel(context.Background(), CancelRequest{RunID: "run_ffffffffffffffffffffffffffffffff"})
	mustCode(t, err, CodeRunNotFound)
}

func timePtr(value time.Time) *time.Time { return &value }

func TestStatusDowngradesFailedPostProcessing(t *testing.T) {
	store := newTestStore(t)
	backend := newFakeBackend()
	service := newTestService(t, store, backend, nil)
	ctx := context.Background()

	started, err := service.Start(ctx, StartRequest{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	runInterface, _ := backend.Get(started.RunID)
	run := runInterface.(*fakeRun)

	report := sampleReport(started.RunID, nil)
	report.Result = diagnostic.ResultNoErrorsObserved
	report.AnalysisComplete = true
	run.set(
		valgrind.AnalysisSnapshot{
			RunID:  started.RunID,
			Phase:  valgrind.PhaseFailed,
			Report: &report,
			Error:  "persist analysis report: quota exceeded",
		},
		jobs.Snapshot{
			SchemaVersion: jobs.SchemaVersion,
			RunID:         started.RunID,
			TargetID:      "fixture",
			State:         jobs.StateCompleted,
		},
	)

	status, err := service.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.AnalysisComplete {
		t.Error("analysis_complete = true after a post-processing failure")
	}
	if status.Result == diagnostic.ResultNoErrorsObserved {
		t.Error("result = no_errors_observed without a published report")
	}
	if status.ReportAvailable {
		t.Error("report_available = true without a persisted report")
	}
	found := false
	for _, warning := range status.Warnings {
		if strings.Contains(warning, "post-processing failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a post-processing warning", status.Warnings)
	}
}

func TestStatusWarningsDoNotLeakReportPaths(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)

	run := createRunDir(t, store)
	manifest := jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion,
		RunID:         run.ID(),
		TargetID:      "fixture",
		State:         jobs.StateCompleted,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.WriteFileAtomic(jobs.ManifestName, data); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(run.Path(), valgrind.AnalysisArtifactName)); err != nil {
		t.Fatal(err)
	}

	status, err := service.Status(context.Background(), StatusRequest{RunID: run.ID()})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	for _, warning := range status.Warnings {
		if strings.Contains(warning, run.Path()) {
			t.Errorf("warning leaks the run path: %q", warning)
		}
	}
}
