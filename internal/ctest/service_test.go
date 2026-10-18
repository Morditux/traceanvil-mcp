package ctest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

func newTestCTestService(t *testing.T) (*Service, *artifacts.Store, *jobs.Manager, config.CTestProject) {
	t.Helper()
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake is not installed")
	}
	ctestBinary, err := exec.LookPath("ctest")
	if err != nil {
		t.Skip("ctest is not installed")
	}
	if !filepath.IsAbs(cmake) || !filepath.IsAbs(ctestBinary) {
		t.Skip("cmake/ctest resolved through a relative PATH entry")
	}
	root := t.TempDir()
	build := filepath.Join(root, "build")
	configure := exec.Command(cmake, "-S", filepath.Join("..", "..", "testdata", "fixtures", "ctest"), "-B", build)
	configure.Env = append([]string{}, "PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C")
	if output, err := configure.CombinedOutput(); err != nil {
		t.Fatalf("configure CTest fixture: %v\n%s", err, output)
	}
	project := config.CTestProject{
		ID:           "fixture",
		TestDir:      build,
		AllowedTests: []string{"pass", "fail", "disabled", "timeout"},
		Profiles:     map[string][]string{"quick": {"pass"}},
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Runner: runner.New(),
		Binary:             func() (string, string, error) { return ctestBinary, "ctest", nil },
		Projects:           []config.CTestProject{project},
		DefaultTestTimeout: 30 * time.Second,
		MaxTimeout:         30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		manager.Close()
	})
	return service, store, manager, project
}

func TestServiceDiscoveryAndTargetedExecution(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	discovery, err := service.Discover(ctx, DiscoverRequest{ProjectID: "fixture"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovery.Tests) != 4 || !discovery.Complete {
		t.Fatalf("discovery = %+v", discovery)
	}

	status, err := service.Start(ctx, RunRequest{
		ProjectID: "fixture",
		Tests:     []string{"pass", "fail", "disabled"},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if status.RunID == "" {
		t.Fatal("Start() returned an empty run id")
	}
	run, ok := service.Get(status.RunID)
	if !ok {
		t.Fatal("live run is missing")
	}
	report, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if report.Result != ResultFailed || !report.Complete {
		t.Fatalf("report = %+v", report)
	}
	if report.Summary.Passed != 1 || report.Summary.Failed != 1 || report.Summary.Skipped != 1 {
		t.Errorf("summary = %+v", report.Summary)
	}
	if len(report.Tests) != 3 || len(report.Artifacts) == 0 {
		t.Errorf("report tests/artifacts = %+v", report)
	}
	artifact, err := service.Artifact(ctx, ArtifactRequest{
		RunID: status.RunID, ArtifactType: ArtifactJUnit, MaxBytes: 300,
	})
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	if artifact.FileName != JUnitArtifactName || !strings.Contains(artifact.Content, "testcase") {
		t.Errorf("artifact = %+v", artifact)
	}
}

func TestFixtureTestsAreNotAutoAdded(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	// The helper allowlist does not contain requires_fixture; build a dedicated
	// project selection without fixture_setup to exercise CTest's auto-add path.
	project := config.CTestProject{
		ID: "fixture-only", TestDir: filepath.Join(filepath.Dir(service.projects["fixture"].TestDir), "build"),
		AllowedTests: []string{"requires_fixture"},
	}
	// Reuse the existing build dir by reading it from the helper project.
	project.TestDir = service.projects["fixture"].TestDir
	service.mu.Lock()
	service.projects["fixture"] = project
	service.mu.Unlock()
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Tests: []string{"requires_fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	report, err := run.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range report.Tests {
		if test.Name == "fixture_setup" {
			t.Fatalf("unlisted fixture setup test was run: %+v", report)
		}
	}
	if report.Result == ResultPassed && len(report.Tests) == 1 {
		t.Logf("fixture regression report: %+v", report)
	}
}

func TestServiceProfileAndPerTestTimeout(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	quick, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(quick.RunID)
	report, err := run.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultPassed || report.Summary.Passed != 1 {
		t.Fatalf("quick report = %+v", report)
	}

	timeout, err := service.Start(ctx, RunRequest{
		ProjectID: "fixture", Tests: []string{"timeout"}, TestTimeoutMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _ = service.Get(timeout.RunID)
	report, err = run.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultFailed || report.Summary.Timeout != 1 {
		t.Fatalf("timeout report = %+v", report)
	}
}

func TestServiceRejectsUnsafeSelections(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	for _, req := range []RunRequest{
		{ProjectID: "fixture", Tests: []string{"not-allowed"}},
		{ProjectID: "fixture", Tests: []string{"pass", "pass"}},
		{ProjectID: "fixture", Profile: "quick", Tests: []string{"pass"}},
		{ProjectID: "fixture", Profile: "missing"},
		{ProjectID: "unknown"},
		{ProjectID: "fixture", TestTimeoutMS: 60000},
	} {
		if _, err := service.Start(ctx, req); err == nil {
			t.Errorf("Start(%+v) accepted an unsafe selection", req)
		}
	}
}

func TestServiceStatusReportAndTerminalCancel(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	if _, err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseCompleted || status.Result != ResultPassed || !status.Complete {
		t.Fatalf("status = %+v", status)
	}
	report, err := service.Report(ctx, ReportRequest{RunID: started.RunID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultPassed || report.Summary.Selected != 1 {
		t.Fatalf("report = %+v", report)
	}
	cancel, err := service.Cancel(ctx, CancelRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if cancel.Canceled {
		t.Errorf("cancel of a completed run = %+v", cancel)
	}
	if _, err := service.Artifact(ctx, ArtifactRequest{
		RunID: started.RunID, ArtifactType: ArtifactReport, MaxBytes: 100,
	}); err != nil {
		t.Errorf("Artifact(report) error = %v", err)
	}
}

func TestServicePersistedReportAndStatus(t *testing.T) {
	service, store, manager, project := newTestCTestService(t)
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	if _, err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	persisted, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Runner: runner.New(),
		Binary:             func() (string, string, error) { return "/usr/bin/ctest", "ctest", nil },
		Projects:           []config.CTestProject{project},
		DefaultTestTimeout: 30 * time.Second, MaxTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(persisted.Close)
	status, err := persisted.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if status.Result != ResultPassed || !status.Complete {
		t.Fatalf("persisted status = %+v", status)
	}
	report, err := persisted.Report(ctx, ReportRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultPassed || len(report.Tests) != 1 {
		t.Fatalf("persisted report = %+v", report)
	}
}

func TestSkippedOnlySelectionIsNotPassed(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Tests: []string{"disabled"}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	report, err := run.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultIncomplete || report.Summary.Skipped != 1 {
		t.Fatalf("skipped-only report = %+v", report)
	}
}

func TestUsableProjectDirRejectsSymlinkOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for _, dir := range []string{root, outside} {
		if err := os.WriteFile(filepath.Join(dir, "CTestTestfile.cmake"), []byte("# fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "linked-build")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	project := config.CTestProject{ID: "fixture", TestDir: link, AllowedTests: []string{"pass"}}
	if usableProjectDir(project, []string{root}) {
		t.Fatal("symlink escaping workspace roots was accepted")
	}
}

func TestTrimIncompleteRuneProgresses(t *testing.T) {
	invalid := []byte{0xff, 'a', 'b'}
	if got := trimIncompleteRune(invalid); len(got) == 0 {
		t.Fatalf("invalid leading byte made the excerpt empty: %q", got)
	}
	truncated := append([]byte("ok"), 0xe2, 0x82)
	if got := trimIncompleteRune(truncated); string(got) != "ok" {
		t.Fatalf("truncated rune excerpt = %q", got)
	}
}

func TestCancelRunningCtest(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Tests: []string{"timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	time.Sleep(100 * time.Millisecond)
	cancel, err := service.Cancel(ctx, CancelRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	report, err := run.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultIncomplete {
		t.Errorf("canceled report = %+v", report)
	}
	status, err := service.Status(ctx, StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseCanceled {
		t.Errorf("canceled status = %+v (cancel=%+v)", status, cancel)
	}
}

func TestCTestAPIRejectsOtherToolRuns(t *testing.T) {
	service, store, _, _ := newTestCTestService(t)
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion, RunID: runDir.ID(), TargetID: "fixture",
		Tool: "memcheck", State: jobs.StateCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(jobs.ManifestName, manifest); err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(StdoutArtifactName, []byte("foreign")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Artifact(ctx, ArtifactRequest{RunID: runDir.ID(), ArtifactType: ArtifactStdout}); CodeOf(err) != CodeRunNotFound {
		t.Errorf("Artifact(other tool) error = %v", err)
	}
	if _, err := service.Status(ctx, StatusRequest{RunID: runDir.ID()}); CodeOf(err) != CodeRunNotFound {
		t.Errorf("Status(other tool) error = %v", err)
	}
	if _, err := service.Report(ctx, ReportRequest{RunID: runDir.ID()}); CodeOf(err) != CodeRunNotFound {
		t.Errorf("Report(other tool) error = %v", err)
	}
}

func TestArtifactTinyWindowAlwaysProgresses(t *testing.T) {
	service, store, _, _ := newTestCTestService(t)
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(StdoutArtifactName, []byte("éabc")); err != nil {
		t.Fatal(err)
	}
	// Register a ctest manifest so the run is recognized by the API.
	if err := runDir.WriteFileAtomic(jobs.ManifestName, []byte(
		`{"schema_version":1,"run_id":"`+runDir.ID()+`","target_id":"fixture","tool":"ctest","state":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	offset := int64(0)
	for i := 0; i < 10; i++ {
		out, err := service.Artifact(context.Background(), ArtifactRequest{
			RunID: runDir.ID(), ArtifactType: ArtifactStdout, Offset: offset, MaxBytes: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.HasMore {
			if out.NextOffset <= offset {
				t.Fatalf("no progress at offset %d: %+v", offset, out)
			}
			offset = out.NextOffset
			continue
		}
		return
	}
	t.Fatal("artifact paging did not terminate")
}

func TestPerTestPropertyCannotBypassConfiguredLimit(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	service.mu.Lock()
	project := service.projects["fixture"]
	project.AllowedTests = append(project.AllowedTests, "property_timeout")
	service.projects["fixture"] = project
	service.mu.Unlock()
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{
		ProjectID: "fixture", Tests: []string{"property_timeout"}, TestTimeoutMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	report, err := run.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultFailed || report.Summary.Timeout != 1 {
		t.Fatalf("property timeout report = %+v", report)
	}
}

func TestServiceHelperBounds(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	if !service.HasUsableProject() {
		t.Error("configured fixture project is not usable")
	}
	if ids := service.ProjectIDs(); len(ids) != 1 || ids[0] != "fixture" {
		t.Errorf("ProjectIDs() = %v", ids)
	}
	if _, err := service.resolveTimeout(-1); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("negative timeout error = %v", err)
	}
	if _, err := service.resolveTimeout(int((service.maxTimeout + time.Second) / time.Millisecond)); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("oversized timeout error = %v", err)
	}
	if _, err := service.resolveTestTimeout(-1); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("negative test timeout error = %v", err)
	}
	started, err := service.Start(context.Background(), RunRequest{ProjectID: "fixture", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	if run.ID() != started.RunID || run.JobSnapshot().RunID != started.RunID {
		t.Errorf("run identity mismatch")
	}
}

func TestReportLimitAndArtifactTypes(t *testing.T) {
	service, _, _, _ := newTestCTestService(t)
	ctx := context.Background()
	started, err := service.Start(ctx, RunRequest{ProjectID: "fixture", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.Get(started.RunID)
	if _, err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	for _, artifactType := range []ArtifactType{ArtifactJUnit, ArtifactStdout, ArtifactStderr, ArtifactReport} {
		out, err := service.Artifact(ctx, ArtifactRequest{
			RunID: started.RunID, ArtifactType: artifactType, MaxBytes: 32,
		})
		if err != nil {
			t.Errorf("Artifact(%s) error = %v", artifactType, err)
			continue
		}
		if out.FileName == "" || out.MaxBytes != 32 {
			t.Errorf("Artifact(%s) = %+v", artifactType, out)
		}
	}
	if _, err := service.Artifact(ctx, ArtifactRequest{
		RunID: started.RunID, ArtifactType: "unknown",
	}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("unknown artifact error = %v", err)
	}
	report := Report{Tests: make([]TestResult, config.MaxCTestAllowedTests+1)}
	limited := limitReport(report, config.MaxCTestAllowedTests+100)
	if len(limited.Tests) != config.MaxCTestAllowedTests || !limited.HasMore {
		t.Errorf("limited report len=%d has_more=%t", len(limited.Tests), limited.HasMore)
	}
	if _, err := service.Cancel(ctx, CancelRequest{RunID: "bad"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("bad cancel id error = %v", err)
	}
}
