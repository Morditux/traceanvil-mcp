package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

type emptyAnalysisBackend struct{}

func (emptyAnalysisBackend) Start(context.Context, valgrind.Request) (analysis.Run, error) {
	return nil, valgrind.ErrBackendUnavailable
}
func (emptyAnalysisBackend) Get(string) (analysis.Run, bool) { return nil, false }

func TestHelgrindAndDRDReportConcurrencyErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		tool valgrind.Tool
		kind string
		raw  string
	}{
		{name: "helgrind", tool: valgrind.ToolHelgrind, kind: "Race", raw: "helgrind.xml"},
		{name: "drd", tool: valgrind.ToolDRD, kind: "ConflictingAccess", raw: "drd.xml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newMemcheckHarness(t, "race", 30*time.Second, 0)
			report, err := h.service.Analyze(context.Background(), valgrind.Request{
				TargetID: "fixture", Tool: test.tool,
			})
			if err != nil {
				t.Fatalf("Analyze(%s) error = %v", test.tool, err)
			}
			if !report.AnalysisComplete {
				t.Fatalf("analysis incomplete: %v", report.Warnings)
			}
			if report.Result != diagnostic.ResultErrorsFound || len(report.Diagnostics) == 0 {
				t.Fatalf("report = %+v", report)
			}
			found := false
			for _, item := range report.Diagnostics {
				if item.Kind == test.kind {
					found = true
				}
				if item.Origin != test.tool.Origin() {
					t.Errorf("origin = %q, want %q", item.Origin, test.tool.Origin())
				}
			}
			if !found {
				t.Errorf("no %s diagnostic in %+v", test.kind, report.Diagnostics)
			}
			hasRaw := false
			for _, artifact := range report.Artifacts {
				if artifact.Name == test.raw {
					hasRaw = true
				}
			}
			if !hasRaw {
				t.Errorf("artifacts = %+v, want raw %s", report.Artifacts, test.raw)
			}
			if value, _ := report.Options["tool"].(string); value != string(test.tool) {
				t.Errorf("options.tool = %q, want %q", value, test.tool)
			}
			if strings.Contains(strings.Join(report.Warnings, " "), "suppressed errors") {
				t.Logf("warnings: %v", report.Warnings)
			}
		})
	}
}

func TestAnalysisHelgrindStatusReportAndRawArtifact(t *testing.T) {
	h := newMemcheckHarness(t, "race", 30*time.Second, 0)
	service := newAnalysisService(t, h, 30*time.Second)
	started, err := service.Start(context.Background(), analysis.StartRequest{
		TargetID: "fixture", Tool: analysis.ToolHelgrind,
	})
	if err != nil {
		t.Fatalf("Start(helgrind) error = %v", err)
	}
	if started.Tool != analysis.ToolHelgrind {
		t.Errorf("status tool = %q", started.Tool)
	}
	final := waitForTerminalPhase(t, service, started.RunID)
	if final.State != analysis.StateCompleted || !final.AnalysisComplete {
		t.Fatalf("final = %+v", final)
	}
	if final.Tool != analysis.ToolHelgrind {
		t.Errorf("final tool = %q", final.Tool)
	}
	page, err := service.Report(context.Background(), analysis.ReportRequest{RunID: started.RunID, Limit: 10})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if page.AnalysisTool != analysis.ToolHelgrind {
		t.Errorf("report analysis tool = %q", page.AnalysisTool)
	}
	if page.Result != diagnostic.ResultErrorsFound || page.Page.Matched == 0 {
		t.Errorf("report = %+v", page)
	}
	artifact, err := service.Artifact(context.Background(), analysis.ArtifactRequest{
		RunID: started.RunID, ArtifactType: analysis.ArtifactRawXML, MaxBytes: 200,
	})
	if err != nil {
		t.Fatalf("Artifact(raw_xml) error = %v", err)
	}
	if artifact.FileName != "helgrind.xml" || !strings.Contains(artifact.Content, "<valgrindoutput") {
		t.Errorf("artifact = %+v", artifact)
	}
}

func TestPersistedHelgrindReportKeepsToolIdentity(t *testing.T) {
	h := newMemcheckHarness(t, "race", 30*time.Second, 0)
	service := newAnalysisService(t, h, 30*time.Second)
	started, err := service.Start(context.Background(), analysis.StartRequest{
		TargetID: "fixture", Tool: analysis.ToolHelgrind,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalPhase(t, service, started.RunID)
	if final.State != analysis.StateCompleted {
		t.Fatalf("final = %+v", final)
	}
	persisted, err := analysis.New(analysis.Options{
		Store: h.store, Backend: emptyAnalysisBackend{},
		BackendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(persisted.Close)
	status, err := persisted.Status(context.Background(), analysis.StatusRequest{RunID: started.RunID})
	if err != nil {
		t.Fatalf("persisted Status() error = %v", err)
	}
	if status.Tool != analysis.ToolHelgrind {
		t.Errorf("persisted tool = %q, want helgrind", status.Tool)
	}
}
