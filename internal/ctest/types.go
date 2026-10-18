package ctest

import (
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
)

const (
	// SchemaVersion versions CTest API payloads and ctest.json.
	SchemaVersion = 1
	// ReportArtifactName is the normalized CTest report.
	ReportArtifactName = "ctest.json"
	// JUnitArtifactName is the raw CTest JUnit report.
	JUnitArtifactName = "ctest.xml"
	// StdoutArtifactName and StderrArtifactName are bounded CTest output.
	StdoutArtifactName = "stdout.log"
	StderrArtifactName = "stderr.log"
	// ToolName identifies CTest runs in persisted metadata.
	ToolName = "ctest"
)

// Phase is one CTest run lifecycle phase.
type Phase string

const (
	PhaseQueued    Phase = "queued"
	PhaseRunning   Phase = "running"
	PhaseAnalyzing Phase = "analyzing"
	PhaseCompleted Phase = "completed"
	PhaseFailed    Phase = "failed"
	PhaseCanceled  Phase = "canceled"
	PhaseTimedOut  Phase = "timed_out"
)

// Result is the aggregate CTest outcome. It is never a claim that tests not
// selected or not discovered passed.
type Result string

const (
	ResultPassed     Result = "passed"
	ResultFailed     Result = "failed"
	ResultIncomplete Result = "incomplete"
	ResultError      Result = "error"
)

// TestStatus is one per-test outcome.
type TestStatus string

const (
	TestPassed  TestStatus = "passed"
	TestFailed  TestStatus = "failed"
	TestTimeout TestStatus = "timeout"
	TestSkipped TestStatus = "skipped"
	TestNotRun  TestStatus = "not_run"
)

// Discovery is the bounded list of declared CTest tests that were found.
type Discovery struct {
	SchemaVersion int      `json:"schema_version"`
	ProjectID     string   `json:"project_id"`
	Tests         []string `json:"tests"`
	Missing       []string `json:"missing,omitempty"`
	Complete      bool     `json:"complete"`
	Warnings      []string `json:"warnings"`
}

// DiscoverRequest selects one trusted project for discovery.
type DiscoverRequest struct {
	ProjectID string `json:"project_id" jsonschema:"declared CTest project id"`
}

// RunRequest selects exact tests or a configured profile and bounded limits.
type RunRequest struct {
	ProjectID     string   `json:"project_id" jsonschema:"declared CTest project id"`
	Tests         []string `json:"tests,omitempty" jsonschema:"exact test names from the project's allowed_tests; omit to select all"`
	Profile       string   `json:"profile,omitempty" jsonschema:"configured profile name; mutually exclusive with tests"`
	TimeoutMS     int      `json:"timeout_ms,omitempty" jsonschema:"optional overall timeout; may only shorten limits.timeout_seconds"`
	TestTimeoutMS int      `json:"test_timeout_ms,omitempty" jsonschema:"optional per-test timeout; may only shorten ctest.test_timeout_seconds"`
}

// StatusRequest selects one run.
type StatusRequest struct {
	RunID string `json:"run_id" jsonschema:"run id returned by ctest_run"`
}

// CancelRequest selects one run to cancel.
type CancelRequest struct {
	RunID string `json:"run_id" jsonschema:"run id returned by ctest_run"`
}

// ReportRequest selects a bounded per-test report page.
type ReportRequest struct {
	RunID string `json:"run_id" jsonschema:"run id returned by ctest_run"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum test results; clamped to the server response bound"`
}

// ArtifactType is one allowlisted CTest artifact.
type ArtifactType string

const (
	ArtifactJUnit  ArtifactType = "junit"
	ArtifactStdout ArtifactType = "stdout"
	ArtifactStderr ArtifactType = "stderr"
	ArtifactReport ArtifactType = "report"
)

// ArtifactRequest selects a bounded artifact excerpt.
type ArtifactRequest struct {
	RunID        string       `json:"run_id" jsonschema:"run id returned by ctest_run"`
	ArtifactType ArtifactType `json:"artifact_type" jsonschema:"one of junit, stdout, stderr, report"`
	Offset       int64        `json:"offset,omitempty" jsonschema:"byte offset"`
	MaxBytes     int          `json:"max_bytes,omitempty" jsonschema:"maximum excerpt bytes"`
}

// ArtifactOutput is a bounded UTF-8 excerpt of one CTest artifact.
type ArtifactOutput struct {
	SchemaVersion       int          `json:"schema_version"`
	RunID               string       `json:"run_id"`
	ArtifactType        ArtifactType `json:"artifact_type"`
	FileName            string       `json:"file_name"`
	Bytes               int64        `json:"bytes"`
	Offset              int64        `json:"offset"`
	ReturnedBytes       int          `json:"returned_bytes"`
	MaxBytes            int          `json:"max_bytes"`
	HasMore             bool         `json:"has_more"`
	NextOffset          int64        `json:"next_offset"`
	Content             string       `json:"content"`
	InvalidUTF8Replaced bool         `json:"invalid_utf8_replaced"`
}

// Summary aggregates selected test statuses.
type Summary struct {
	Selected int `json:"selected"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Timeout  int `json:"timeout"`
	Skipped  int `json:"skipped"`
	NotRun   int `json:"not_run"`
}

// TestResult is one selected test and its association to the raw JUnit report.
type TestResult struct {
	Name       string     `json:"name"`
	Status     TestStatus `json:"status"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	Message    string     `json:"message,omitempty"`
}

// Status is the lifecycle/status payload for one CTest run.
type Status struct {
	SchemaVersion int        `json:"schema_version"`
	RunID         string     `json:"run_id"`
	ProjectID     string     `json:"project_id"`
	Tool          string     `json:"tool"`
	State         jobs.State `json:"state"`
	Phase         Phase      `json:"phase"`
	Result        Result     `json:"result,omitempty"`
	Complete      bool       `json:"complete"`
	Summary       Summary    `json:"summary"`
	StartedAt     string     `json:"started_at,omitempty"`
	FinishedAt    string     `json:"finished_at,omitempty"`
	ElapsedMS     int64      `json:"elapsed_ms"`
	Error         string     `json:"error,omitempty"`
	Warnings      []string   `json:"warnings"`
}

// Report is a bounded per-test report plus raw artifact references.
type Report struct {
	SchemaVersion int                   `json:"schema_version"`
	RunID         string                `json:"run_id"`
	ProjectID     string                `json:"project_id"`
	Tool          string                `json:"tool"`
	Result        Result                `json:"result"`
	Complete      bool                  `json:"complete"`
	Summary       Summary               `json:"summary"`
	Selected      []string              `json:"selected"`
	Tests         []TestResult          `json:"tests"`
	Artifacts     []diagnostic.Artifact `json:"artifacts"`
	Warnings      []string              `json:"warnings"`
	HasMore       bool                  `json:"has_more"`
}

// JUnitIssue is one parsed testcase before association.
type junitIssue struct {
	Name       string
	Status     TestStatus
	DurationMS int64
	Message    string
}

// CancelResult is returned after a cancel request.
type CancelResult struct {
	SchemaVersion int        `json:"schema_version"`
	RunID         string     `json:"run_id"`
	Canceled      bool       `json:"canceled"`
	State         jobs.State `json:"state"`
	Phase         Phase      `json:"phase"`
}
