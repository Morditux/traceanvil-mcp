package profile

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

func profileFixture(t *testing.T) (root, program string) {
	t.Helper()
	root = t.TempDir()
	program = filepath.Join(root, "profile-target")
	source := filepath.Join(root, "profile-target.c")
	code := `#include <stdlib.h>
#include <string.h>
static volatile unsigned long sink;
static void work_a(unsigned n) { for (unsigned i = 0; i < n; i++) sink += i; }
static void work_b(unsigned n) { for (unsigned i = 0; i < n; i++) sink ^= (i * 3u); }
int main(void) {
  work_a(1000);
  work_b(2000);
  unsigned char *buf = malloc(1024 * 1024);
  if (!buf) return 1;
  memset(buf, 1, 1024 * 1024);
  sink += buf[0];
  free(buf);
  return 0;
}
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-fno-omit-frame-pointer", "-o", program, source).CombinedOutput(); err != nil {
		t.Skipf("compile profile fixture: %v\n%s", err, output)
	}
	return root, program
}

func newProfileTestService(t *testing.T, root string) (*Service, *jobs.Manager) {
	t.Helper()
	valgrindPath, err := exec.LookPath("valgrind")
	if err != nil || !filepath.IsAbs(valgrindPath) {
		t.Skip("valgrind is not installed at an absolute path")
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: filepath.Join(root, "profile-target"),
			Cwd: root, AllowedArgs: []string{},
		}},
	}
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: policy.New(cfg),
		BinaryProvider: func() (string, string, error) { return valgrindPath, "valgrind test", nil },
		MaxTimeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, manager
}

func TestRunCallgrindAndMassifRealFixtures(t *testing.T) {
	root, _ := profileFixture(t)
	service, manager := newProfileTestService(t, root)
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	callgrind, err := service.Run(ctx, ToolCallgrind, Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Run(callgrind) error = %v", err)
	}
	if !callgrind.Complete {
		t.Fatalf("callgrind report incomplete: %+v", callgrind)
	}
	if callgrind.ProgramSHA256 == "" {
		t.Fatal("callgrind report has no program hash")
	}
	if len(callgrind.Events) == 0 || callgrind.Events[0].Name != "Ir" {
		t.Fatalf("callgrind events = %+v", callgrind.Events)
	}
	if len(callgrind.TopFunctions) == 0 || callgrind.Metrics["total"] <= 0 {
		t.Fatalf("callgrind metrics/top = %+v / %+v", callgrind.Metrics, callgrind.TopFunctions)
	}
	var sawA, sawB bool
	for _, function := range callgrind.TopFunctions {
		sawA = sawA || function.Name == "work_a"
		sawB = sawB || function.Name == "work_b"
	}
	if !sawA || !sawB {
		t.Fatalf("callgrind top functions do not contain fixture work: %+v", callgrind.TopFunctions)
	}
	artifact, err := service.Artifact(context.Background(), ArtifactRequest{
		RunID: callgrind.RunID, FileName: "callgrind.out", MaxBytes: 512,
	})
	if err != nil || artifact.Content == "" {
		t.Fatalf("callgrind artifact = %+v, %v", artifact, err)
	}

	massif, err := service.Run(ctx, ToolMassif, Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Run(massif) error = %v", err)
	}
	if !massif.Complete || massif.PeakHeapBytes < 1024*1024 {
		t.Fatalf("massif report = %+v", massif)
	}
	if massif.Snapshots == 0 || len(massif.SnapshotSamples) == 0 {
		t.Fatalf("massif snapshots = %+v", massif)
	}
	massifAgain, err := service.Run(ctx, ToolMassif, Request{TargetID: "fixture"})
	if err != nil || !massifAgain.Complete || massifAgain.PeakHeapBytes != massif.PeakHeapBytes {
		t.Fatalf("massif reproducibility = %+v, %v", massifAgain, err)
	}

	callgrindAgain, err := service.Run(ctx, ToolCallgrind, Request{TargetID: "fixture"})
	if err != nil || !callgrindAgain.Complete {
		t.Fatalf("second callgrind run = %+v, %v", callgrindAgain, err)
	}
	if callgrindAgain.Metrics["total"] != callgrind.Metrics["total"] {
		t.Fatalf("callgrind totals not reproducible: %v != %v",
			callgrindAgain.Metrics["total"], callgrind.Metrics["total"])
	}
	comparison, err := service.Compare(context.Background(), callgrind.RunID, callgrindAgain.RunID)
	if err != nil || !comparison.Compatible {
		t.Fatalf("Compare() = %+v, %v", comparison, err)
	}
	if comparison.Deltas["total"] != 0 {
		t.Fatalf("callgrind comparison delta = %v", comparison.Deltas["total"])
	}
	if _, err := service.Compare(context.Background(), callgrind.RunID, massif.RunID); CodeOf(err) != CodeIncompatibleProfiles {
		t.Fatalf("cross-tool Compare() error = %v", err)
	}
}

func TestRunRejectsUnknownTargetAndTimeout(t *testing.T) {
	root, _ := profileFixture(t)
	service, manager := newProfileTestService(t, root)
	defer manager.Close()
	if _, err := service.Run(context.Background(), ToolCallgrind, Request{TargetID: "missing"}); CodeOf(err) != CodeTargetUnknown {
		t.Fatalf("unknown target error = %v", err)
	}
	if _, err := service.Run(context.Background(), ToolMassif, Request{TargetID: "fixture", TimeoutMS: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("negative timeout error = %v", err)
	}
	if _, err := service.Run(context.Background(), Tool("perf"), Request{TargetID: "fixture"}); CodeOf(err) != CodeUnsupported {
		t.Fatalf("unsupported tool error = %v", err)
	}
}

func TestCompareMassifTimeUnitMismatchIsRefused(t *testing.T) {
	service, manager := newProfileTestService(t, t.TempDir())
	defer manager.Close()
	base := Report{SchemaVersion: APISchemaVersion, Tool: ToolMassif, Complete: true, TimeUnit: "i",
		Metrics: map[string]float64{"heap_peak_bytes": 1}}
	baseID, err := writeTestProfile(service, base)
	if err != nil {
		t.Fatal(err)
	}
	target := base
	target.TimeUnit = "B"
	targetID, err := writeTestProfile(service, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Compare(context.Background(), baseID, targetID); CodeOf(err) != CodeIncompatibleProfiles {
		t.Fatalf("time-unit mismatch error = %v", err)
	}
}

func TestCompareEventMismatchIsRefused(t *testing.T) {
	service, manager := newProfileTestService(t, t.TempDir())
	defer manager.Close()
	base := Report{
		SchemaVersion: APISchemaVersion, Tool: ToolCallgrind, Complete: true,
		Events: []EventMetric{{Name: "Ir", Total: 1}}, Metrics: map[string]float64{"total": 1},
	}
	baseID, err := writeTestProfile(service, base)
	if err != nil {
		t.Fatal(err)
	}
	target := base
	target.Events = []EventMetric{{Name: "Dr", Total: 1}}
	targetID, err := writeTestProfile(service, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Compare(context.Background(), baseID, targetID); CodeOf(err) != CodeIncompatibleProfiles {
		t.Fatalf("event mismatch error = %v", err)
	}
}

func writeTestProfile(service *Service, report Report) (string, error) {
	runDir, err := service.store.Create()
	if err != nil {
		return "", err
	}
	report.RunID = runDir.ID()
	data, err := jsonMarshal(report)
	if err != nil {
		return "", err
	}
	if err := runDir.WriteFileAtomic(ReportArtifactName, data); err != nil {
		return "", err
	}
	return report.RunID, nil
}

func jsonMarshal(report Report) ([]byte, error) {
	return json.Marshal(report)
}

func TestProfileTimeoutAndCancellation(t *testing.T) {
	valgrindPath, err := exec.LookPath("valgrind")
	if err != nil || !filepath.IsAbs(valgrindPath) {
		t.Skip("valgrind is not installed at an absolute path")
	}
	root := t.TempDir()
	target := filepath.Join(root, "sleep")
	data, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skipf("read /bin/sleep: %v", err)
	}
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "sleep", Program: target, Cwd: root, AllowedArgs: []string{"30"},
		}},
	}
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: policy.New(cfg),
		BinaryProvider: func() (string, string, error) { return valgrindPath, "test", nil },
		MaxTimeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background(), ToolCallgrind, Request{
		TargetID: "sleep", Args: []string{"30"}, TimeoutMS: 50,
	})
	if err != nil {
		t.Fatalf("timeout Run() error = %v", err)
	}
	if report.Complete || !report.TimedOut {
		t.Fatalf("timeout report = %+v", report)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	defer cancel()
	report, err = service.Run(ctx, ToolMassif, Request{TargetID: "sleep", Args: []string{"30"}})
	if CodeOf(err) != CodeCanceled {
		t.Fatalf("cancel Run() error = %v", err)
	}
	if report.Complete {
		t.Fatalf("canceled profile was complete: %+v", report)
	}
}

func TestRunRejectsTargetValgrindOptionsFile(t *testing.T) {
	root, _ := profileFixture(t)
	service, manager := newProfileTestService(t, root)
	defer manager.Close()
	if err := os.WriteFile(filepath.Join(root, ".valgrindrc"), []byte("--tool=massif\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background(), ToolCallgrind, Request{TargetID: "fixture"}); CodeOf(err) != CodeTargetDenied {
		t.Fatalf("untrusted .valgrindrc error = %v", err)
	}
}
