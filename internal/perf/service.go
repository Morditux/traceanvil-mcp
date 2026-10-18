package perf

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// ServiceOptions wires the experimental perf runner.
type ServiceOptions struct {
	Store          *artifacts.Store
	Manager        *jobs.Manager
	Resolver       *policy.Resolver
	BinaryProvider func() (binary, version string, err error)
	MaxTimeout     time.Duration
}

// Service runs one fixed perf stat command at a time.
type Service struct {
	store      *artifacts.Store
	manager    *jobs.Manager
	resolver   *policy.Resolver
	provider   func() (string, string, error)
	maxTimeout time.Duration
}

// NewService validates the shared wiring.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil || opts.Manager == nil || opts.Resolver == nil || opts.BinaryProvider == nil {
		return nil, errors.New("perf: incomplete service wiring")
	}
	return &Service{store: opts.Store, manager: opts.Manager, resolver: opts.Resolver,
		provider: opts.BinaryProvider, maxTimeout: opts.MaxTimeout}, nil
}

// HasUsableTarget reports whether a declared target currently resolves.
func (s *Service) HasUsableTarget() bool { return s.resolver.HasUsableTarget() }

// Run executes perf stat with the fixed event list. It never accepts a perf
// flag or event from the client.
func (s *Service) Run(ctx context.Context, req Request) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, ContextError(err)
	}
	if strings.TrimSpace(req.TargetID) == "" {
		return Report{}, Errorf(CodeInvalidArgument, "target_id must not be empty")
	}
	timeout, err := s.resolveTimeout(req.TimeoutMS)
	if err != nil {
		return Report{}, err
	}
	binary, version, err := s.resolveBackend()
	if err != nil {
		return Report{}, err
	}
	resolved, err := s.resolver.Resolve(req.TargetID, req.Args)
	if err != nil {
		var denied *policy.DeniedError
		if errors.As(err, &denied) && denied.Reason == policy.ReasonUnknownTarget {
			return Report{}, Wrap(CodeTargetUnknown, err, "target %q is not declared", req.TargetID)
		}
		return Report{}, Wrap(CodeTargetDenied, err, "target %q is denied by policy", req.TargetID)
	}
	runDir, err := s.store.Create()
	if err != nil {
		return Report{}, Wrap(CodeRunFailed, err, "create perf run directory")
	}
	statPath := filepath.Join(runDir.Path(), "perf-stat.json")
	args := []string{"stat", "-j", "-o", statPath, "-e",
		"task-clock,cycles,instructions", "--", resolved.Program}
	args = append(args, resolved.Args...)
	snapshot, err := s.start(ctx, runDir, resolved, binary, args, timeout)
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return Report{}, err
	}
	adopted, adoptErr := runDir.Adopt("perf-stat.json", 1<<20)
	report := s.buildReport(runDir, resolved, binary, version, snapshot, adopted, adoptErr)
	if err := writeReport(runDir, &report); err != nil {
		return report, Wrap(CodeRunFailed, err, "write perf report")
	}
	if ctx.Err() != nil {
		return report, ContextError(ctx.Err())
	}
	return report, nil
}

func (s *Service) start(ctx context.Context, runDir *artifacts.RunDir, target policy.Resolved,
	binary string, args []string, timeout time.Duration) (jobs.Snapshot, error) {
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID: target.TargetID, Tool: "profile_perf", Executable: binary,
		Args: args, Dir: target.Dir, Env: policy.Environment(""), Timeout: timeout,
	})
	if err != nil {
		return jobs.Snapshot{}, Wrap(CodeRunFailed, err, "start perf stat")
	}
	snapshot, waitErr := job.Wait(ctx)
	if waitErr != nil {
		job.Cancel()
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		snapshot, _ = job.Wait(drainCtx)
		cancel()
	}
	return snapshot, nil
}

func (s *Service) buildReport(runDir *artifacts.RunDir, target policy.Resolved,
	binary, version string, snapshot jobs.Snapshot, adopted artifacts.AdoptedFile, adoptErr error) Report {
	report := Report{
		SchemaVersion: APISchemaVersion, RunID: runDir.ID(), TargetID: target.TargetID,
		Program: target.Program, Args: append([]string(nil), target.Args...),
		ToolPath: binary, ToolVersion: version, Experimental: true,
		Metrics: map[string]float64{}, Artifacts: []Artifact{}, Warnings: []string{},
		ReportArtifact: "perf.json",
	}
	if snapshot.Result != nil {
		report.ExitCode = snapshot.Result.ExitCode
		report.TimedOut = snapshot.Result.TimedOut
		report.Canceled = snapshot.Result.Canceled
		report.DurationMS = snapshot.Result.DurationMS
	}
	multiplexed := false
	if adoptErr != nil {
		report.Warnings = append(report.Warnings, "perf stat output is unavailable: "+adoptErr.Error())
	} else if adopted.Name != "" {
		rawBytes, rawErr := runDir.ReadFile("perf-stat.json")
		if rawErr != nil {
			report.Warnings = append(report.Warnings, "perf stat output is unreadable: "+rawErr.Error())
		} else {
			parsed, parseErr := ParseStat(strings.NewReader(string(rawBytes)))
			if parseErr != nil {
				report.Warnings = append(report.Warnings, "perf output parse failed: "+parseErr.Error())
			} else {
				report.Events = parsed.Events
				report.Warnings = append(report.Warnings, parsed.Warnings...)
				for _, event := range parsed.Events {
					report.Metrics[sanitize(event.Name)] = event.Value
					if event.RunningPercent < 100 {
						multiplexed = true
						report.Warnings = append(report.Warnings,
							"perf counter "+event.Name+" was multiplexed or partially running")
					}
				}
			}
		}
		report.Artifacts = append(report.Artifacts, Artifact{Name: "perf-stat.json", Kind: "perf_json", Bytes: adopted.Bytes, Truncated: adopted.Truncated})
	}
	if stdoutBytes, err := runDir.ReadFile("stdout.log"); err == nil {
		report.Artifacts = append(report.Artifacts, Artifact{Name: "stdout.log", Kind: "raw_perf_stdout", Bytes: int64(len(stdoutBytes))})
	}
	if stderrBytes, err := runDir.ReadFile("stderr.log"); err == nil {
		report.Artifacts = append(report.Artifacts, Artifact{Name: "stderr.log", Kind: "raw_perf_stderr", Bytes: int64(len(stderrBytes))})
		if len(stderrBytes) > 0 && snapshot.Result != nil && snapshot.Result.ExitCode != 0 {
			report.Warnings = append(report.Warnings, "perf stderr is preserved in the artifact")
		}
	}
	missing := missingFixedEvents(report.Events)
	if len(missing) > 0 {
		report.Warnings = append(report.Warnings, "fixed perf events missing: "+strings.Join(missing, ", "))
	}
	report.Complete = snapshot.State == jobs.StateCompleted && snapshot.Result != nil &&
		snapshot.Result.Started && !snapshot.Result.TimedOut && !snapshot.Result.Canceled &&
		snapshot.Result.ExitCode == 0 && adoptErr == nil && !adopted.Truncated &&
		len(report.Events) > 0 && len(missing) == 0 && !multiplexed
	if snapshot.Result == nil {
		report.Warnings = append(report.Warnings, "program result is missing")
		report.Complete = false
	}
	if snapshot.Error != "" {
		report.Warnings = append(report.Warnings, "job error: "+snapshot.Error)
		report.Complete = false
	}
	if snapshot.Result != nil && snapshot.Result.ExitCode != 0 {
		report.Warnings = append(report.Warnings,
			"target or perf exited nonzero; this experimental result is incomplete")
	}
	if snapshot.Result != nil && snapshot.Result.Signal != 0 {
		report.Warnings = append(report.Warnings, "target terminated by a signal")
		report.Complete = false
	}
	if snapshot.Result != nil && (snapshot.Result.StdoutTruncated || snapshot.Result.StderrTruncated) {
		report.Warnings = append(report.Warnings, "perf output exceeded the collection bound")
		report.Complete = false
	}
	if len(report.Events) == 0 {
		report.Warnings = append(report.Warnings, "no numeric perf events were observed")
	}
	return report
}

func missingFixedEvents(events []EventMetric) []string {
	present := make(map[string]bool, len(events))
	for _, event := range events {
		present[event.Name] = true
	}
	var missing []string
	for _, name := range []string{"task-clock", "cycles", "instructions"} {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func (s *Service) resolveTimeout(timeoutMS int) (time.Duration, error) {
	if timeoutMS < 0 {
		return 0, Errorf(CodeInvalidArgument, "timeout_ms must not be negative")
	}
	if timeoutMS == 0 {
		return 0, nil
	}
	requested := time.Duration(timeoutMS) * time.Millisecond
	if s.maxTimeout > 0 && requested > s.maxTimeout {
		return 0, Errorf(CodeInvalidArgument, "timeout_ms exceeds the configured maximum")
	}
	return requested, nil
}

func (s *Service) resolveBackend() (string, string, error) {
	binary, version, err := s.provider()
	if err != nil {
		return "", "", Wrap(CodeBackendUnavailable, err, "perf is unavailable")
	}
	if !filepath.IsAbs(binary) {
		return "", "", Errorf(CodeBackendUnavailable, "perf path is not absolute")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", Errorf(CodeBackendUnavailable, "perf executable is not usable")
	}
	return binary, version, nil
}

func writeReport(runDir *artifacts.RunDir, report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > DefaultMaxReportBytes {
		return Errorf(CodeRunFailed, "perf report exceeds its bound")
	}
	return runDir.WriteFileAtomic("perf.json", data)
}

// Load reads a persisted perf report.
func (s *Service) Load(ctx context.Context, runID string) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, ContextError(err)
	}
	file, err := s.store.OpenArtifact(runID, "perf.json")
	if err != nil {
		return Report{}, Wrap(CodeProfileIncomplete, err, "open perf report")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, DefaultMaxReportBytes+1))
	if err != nil || int64(len(data)) > DefaultMaxReportBytes {
		return Report{}, Errorf(CodeProfileIncomplete, "perf report is unreadable or too large")
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, Wrap(CodeProfileIncomplete, err, "decode perf report")
	}
	if report.SchemaVersion != APISchemaVersion || report.RunID != runID {
		return Report{}, Errorf(CodeProfileIncomplete, "perf report identity is invalid")
	}
	return report, nil
}

// Artifact returns a bounded excerpt of a preserved perf artifact.
func (s *Service) Artifact(ctx context.Context, req ArtifactRequest) (ArtifactOutput, error) {
	if ctx == nil || req.RunID == "" {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "run_id is required")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, ContextError(err)
	}
	report, err := s.Load(ctx, req.RunID)
	if err != nil {
		return ArtifactOutput{}, err
	}
	allowed := map[string]bool{"perf.json": true}
	for _, artifact := range report.Artifacts {
		allowed[artifact.Name] = true
	}
	if !allowed[req.FileName] {
		return ArtifactOutput{}, Errorf(CodeArtifactNotFound, "artifact %q is not part of this perf run", req.FileName)
	}
	if req.Offset < 0 || req.MaxBytes < 0 {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "offset and max_bytes must not be negative")
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxArtifactBytes
	}
	if maxBytes > MaxArtifactBytes {
		maxBytes = MaxArtifactBytes
	}
	file, err := s.store.OpenArtifact(req.RunID, req.FileName)
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "open perf artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || req.Offset > info.Size() {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "invalid artifact offset")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, req.Offset, int64(maxBytes)))
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "read perf artifact")
	}
	if req.Offset+int64(len(data)) < info.Size() {
		raw := data
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		if len(data) == 0 && len(raw) > 0 {
			data = raw[:1]
		}
	}
	return ArtifactOutput{SchemaVersion: APISchemaVersion, RunID: req.RunID,
		FileName: req.FileName, Bytes: info.Size(), Offset: req.Offset,
		ReturnedBytes: len(data), MaxBytes: maxBytes,
		HasMore:    req.Offset+int64(len(data)) < info.Size(),
		NextOffset: req.Offset + int64(len(data)),
		Content:    strings.ToValidUTF8(string(data), "\uFFFD")}, nil
}
