package vgdb

import (
	"bytes"
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
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

// ServiceOptions wires the fixed vgdb prototype.
type ServiceOptions struct {
	Store            *artifacts.Store
	Manager          *jobs.Manager
	Resolver         *policy.Resolver
	ValgrindProvider func() (binary, version string, err error)
	VgdbProvider     func() (binary, version string, err error)
	Runner           *runner.Runner
	MaxTimeout       time.Duration
}

// Service runs one fixed Valgrind+vgdb inspection at a time.
type Service struct {
	store      *artifacts.Store
	manager    *jobs.Manager
	resolver   *policy.Resolver
	valgrind   func() (string, string, error)
	vgdb       func() (string, string, error)
	runner     *runner.Runner
	maxTimeout time.Duration
}

// NewService validates the shared wiring.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil || opts.Manager == nil || opts.Resolver == nil ||
		opts.ValgrindProvider == nil || opts.VgdbProvider == nil {
		return nil, errors.New("vgdb: incomplete service wiring")
	}
	r := opts.Runner
	if r == nil {
		r = runner.New()
	}
	return &Service{store: opts.Store, manager: opts.Manager, resolver: opts.Resolver,
		valgrind: opts.ValgrindProvider, vgdb: opts.VgdbProvider, runner: r,
		maxTimeout: opts.MaxTimeout}, nil
}

// HasUsableTarget reports whether a declared target currently resolves.
func (s *Service) HasUsableTarget() bool { return s.resolver.HasUsableTarget() }

// Run starts Valgrind with vgdb enabled, sends only these fixed read-only
// commands: v.info gdbserver_status, v.info exectxt and v.info scheduler.
// Cleanup uses v.kill; there is no client-provided monitor command.
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
	valgrindPath, _, err := s.resolveBinary(s.valgrind, "Valgrind")
	if err != nil {
		return Report{}, err
	}
	vgdbPath, _, err := s.resolveBinary(s.vgdb, "vgdb")
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
		return Report{}, Wrap(CodeRunFailed, err, "create vgdb run directory")
	}
	prefix := filepath.Join(runDir.Path(), "vgdb")
	defer cleanupVgdbPrefix(runDir.Path(), filepath.Base(prefix))
	args := []string{
		"--tool=memcheck", "--vgdb=yes", "--vgdb-error=0",
		"--vgdb-prefix=" + prefix, "--", resolved.Program,
	}
	args = append(args, resolved.Args...)
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID: resolved.TargetID, Tool: "vgdb_inspect", Executable: valgrindPath,
		Args: args, Dir: resolved.Dir, Env: policy.Environment(""), Timeout: timeout,
	})
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return Report{}, Wrap(CodeRunFailed, err, "start Valgrind vgdb session")
	}
	report := Report{
		SchemaVersion: APISchemaVersion, RunID: runDir.ID(), TargetID: resolved.TargetID,
		Program: resolved.Program, Args: append([]string(nil), resolved.Args...),
		ValgrindPath: valgrindPath, VgdbPath: vgdbPath, Experimental: true,
		Commands: []CommandResult{}, Artifacts: []Artifact{}, Warnings: []string{},
		ReportArtifact: "vgdb.json",
	}
	fixed := []string{
		"v.info gdbserver_status",
		"v.info exectxt",
		"v.info scheduler",
	}
	commandCtx, cancelCommands := context.WithTimeout(ctx, commandBudget(timeout))
	ready := s.waitForServer(commandCtx, job, prefix)
	if !ready {
		report.Warnings = append(report.Warnings, "Valgrind vgdb server did not become ready")
	}
	if ready {
		for _, command := range fixed {
			if jobTerminal(job.Snapshot().State) {
				report.Warnings = append(report.Warnings, "Valgrind job ended before fixed command "+command)
				break
			}
			result := s.runCommand(commandCtx, vgdbPath, prefix, resolved.Dir, command, true)
			report.Commands = append(report.Commands, result)
		}
	}
	cancelCommands()

	snapshot := job.Snapshot()
	cleanupCanceled := false
	killComplete := true
	if !jobTerminal(snapshot.State) {
		killResult := s.runCommand(context.Background(), vgdbPath, prefix, resolved.Dir, "v.kill", false)
		killComplete = killResult.Complete
		if !killResult.Complete {
			report.Warnings = append(report.Warnings,
				"fixed v.kill cleanup was incomplete; process-group cancellation follows")
		}
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
		snapshot, _ = job.Wait(drainCtx)
		drainCancel()
		if !jobTerminal(snapshot.State) {
			cleanupCanceled = true
			job.Cancel()
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			snapshot, _ = job.Wait(cancelCtx)
			cancel()
			report.CleanupMethod = "process-group"
		} else {
			report.CleanupMethod = "v.kill"
		}
	} else {
		report.CleanupMethod = "process-exit"
	}
	if snapshot.Result != nil {
		report.ExitCode = snapshot.Result.ExitCode
		report.TimedOut = snapshot.Result.TimedOut
		report.Canceled = snapshot.Result.Canceled
		report.DurationMS = snapshot.Result.DurationMS
	}
	allCommands := len(report.Commands) == len(fixed)
	for _, command := range report.Commands {
		if !command.Complete {
			allCommands = false
		}
	}
	report.Complete = ready && allCommands && killComplete && !cleanupCanceled &&
		snapshot.State == jobs.StateCompleted && snapshot.Error == "" && ctx.Err() == nil
	if snapshot.Error != "" {
		report.Warnings = append(report.Warnings, "Valgrind job error: "+snapshot.Error)
	}
	if snapshot.Result != nil && (snapshot.Result.StdoutTruncated || snapshot.Result.StderrTruncated) {
		report.Warnings = append(report.Warnings, "Valgrind output exceeded the collection bound")
		report.Complete = false
	}
	if ctx.Err() != nil {
		report.Complete = false
		report.Warnings = append(report.Warnings, "vgdb inspection context ended before a clean cleanup")
	}
	// Preserve the Valgrind logs and the normalized report reference.
	if stdoutBytes, err := runDir.ReadFile("stdout.log"); err == nil {
		report.Artifacts = append(report.Artifacts, Artifact{Name: "stdout.log", Kind: "raw_valgrind_stdout", Bytes: int64(len(stdoutBytes))})
	}
	if stderrBytes, err := runDir.ReadFile("stderr.log"); err == nil {
		report.Artifacts = append(report.Artifacts, Artifact{Name: "stderr.log", Kind: "raw_valgrind_stderr", Bytes: int64(len(stderrBytes))})
	}
	if err := writeReport(runDir, &report); err != nil {
		return report, Wrap(CodeRunFailed, err, "write vgdb report")
	}
	if ctx.Err() != nil {
		return report, ContextError(ctx.Err())
	}
	return report, nil
}

func (s *Service) runCommand(ctx context.Context, binary, prefix, dir, command string, requireOutput bool) CommandResult {
	result := CommandResult{Command: command}
	var stdout, stderr bytes.Buffer
	runResult, err := s.runner.Run(ctx, runner.Request{
		Executable: binary,
		Args: []string{
			"--vgdb-prefix=" + prefix, "--wait=3", "--cmd-time-out=3", command,
		},
		Dir: dir, Env: policy.Environment(""),
		Timeout: 8 * time.Second, MaxOutputBytes: DefaultMaxOutputBytes,
		Stdout: &stdout, Stderr: &stderr,
	})
	result.ExitCode = runResult.ExitCode
	if err != nil {
		result.Output = boundedText(stdout.String(), stderr.String())
		result.Truncated = runResult.StdoutTruncated || runResult.StderrTruncated
		return result
	}
	result.Output = boundedText(stdout.String(), stderr.String())
	result.Truncated = runResult.StdoutTruncated || runResult.StderrTruncated
	result.Complete = !runResult.TimedOut && !runResult.Canceled && !result.Truncated &&
		runResult.ExitCode == 0 && (!requireOutput || strings.TrimSpace(result.Output) != "")
	return result
}

func boundedText(stdout, stderr string) string {
	combined := stdout
	if stderr != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr
	}
	combined = strings.ToValidUTF8(combined, "\uFFFD")
	if len(combined) > int(DefaultMaxOutputBytes) {
		combined = combined[:DefaultMaxOutputBytes]
		for len(combined) > 0 && !utf8.ValidString(combined) {
			combined = combined[:len(combined)-1]
		}
	}
	return combined
}

func jobTerminal(state jobs.State) bool {
	switch state {
	case jobs.StateCompleted, jobs.StateCancelled, jobs.StateFailed, jobs.StateTimedOut:
		return true
	default:
		return false
	}
}

func (s *Service) waitForServer(ctx context.Context, job *jobs.Job, prefix string) bool {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if jobTerminal(job.Snapshot().State) {
			return false
		}
		if matches, _ := filepath.Glob(prefix + "-*"); len(matches) > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func cleanupVgdbPrefix(dir, base string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), base+"-") {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func commandBudget(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > 30*time.Second {
		return 30 * time.Second
	}
	return timeout
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

func (s *Service) resolveBinary(provider func() (string, string, error), name string) (string, string, error) {
	binary, version, err := provider()
	if err != nil {
		return "", "", Wrap(CodeBackendUnavailable, err, "%s is unavailable", name)
	}
	if !filepath.IsAbs(binary) {
		return "", "", Errorf(CodeBackendUnavailable, "%s path is not absolute", name)
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", Errorf(CodeBackendUnavailable, "%s executable is not usable", name)
	}
	return binary, version, nil
}

func writeReport(runDir *artifacts.RunDir, report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > DefaultMaxReportBytes {
		return Errorf(CodeRunFailed, "vgdb report exceeds its bound")
	}
	return runDir.WriteFileAtomic("vgdb.json", data)
}

// Load reads a persisted vgdb report.
func (s *Service) Load(ctx context.Context, runID string) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, ContextError(err)
	}
	file, err := s.store.OpenArtifact(runID, "vgdb.json")
	if err != nil {
		return Report{}, Wrap(CodeProfileIncomplete, err, "open vgdb report")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, DefaultMaxReportBytes+1))
	if err != nil || int64(len(data)) > DefaultMaxReportBytes {
		return Report{}, Errorf(CodeProfileIncomplete, "vgdb report is unreadable or too large")
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, Wrap(CodeProfileIncomplete, err, "decode vgdb report")
	}
	if report.SchemaVersion != APISchemaVersion || report.RunID != runID {
		return Report{}, Errorf(CodeProfileIncomplete, "vgdb report identity is invalid")
	}
	return report, nil
}

// Artifact returns a bounded excerpt of preserved vgdb evidence.
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
	allowed := map[string]bool{"vgdb.json": true}
	for _, artifact := range report.Artifacts {
		allowed[artifact.Name] = true
	}
	if !allowed[req.FileName] {
		return ArtifactOutput{}, Errorf(CodeArtifactNotFound, "artifact %q is not part of this vgdb run", req.FileName)
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
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "open vgdb artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || req.Offset > info.Size() {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "invalid artifact offset")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, req.Offset, int64(maxBytes)))
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "read vgdb artifact")
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
