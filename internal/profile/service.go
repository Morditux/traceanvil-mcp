package profile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// ServiceOptions wires the profiler to the shared artifact store and job
// manager. The manager and store remain owned by the caller.
type ServiceOptions struct {
	Store          *artifacts.Store
	Manager        *jobs.Manager
	Resolver       *policy.Resolver
	Roots          []string
	BinaryProvider func() (binary, version string, err error)
	// ToolAvailable revalidates the tool-specific capability immediately
	// before a run. Nil means all implemented profilers are allowed.
	ToolAvailable func(Tool) (bool, string)

	MaxRawBytes   int64
	MaxTimeout    time.Duration
	WatchInterval time.Duration
}

// Service runs Callgrind/Massif profiles synchronously and stores all evidence.
type Service struct {
	store         *artifacts.Store
	manager       *jobs.Manager
	resolver      *policy.Resolver
	roots         []string
	provider      func() (string, string, error)
	toolAvailable func(Tool) (bool, string)
	maxRaw        int64
	maxTimeout    time.Duration
	watchInterval time.Duration
}

// NewService validates the shared wiring.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("profile: nil artifact store")
	}
	if opts.Manager == nil {
		return nil, errors.New("profile: nil job manager")
	}
	if opts.Resolver == nil {
		return nil, errors.New("profile: nil policy resolver")
	}
	if opts.BinaryProvider == nil {
		return nil, errors.New("profile: nil Valgrind binary provider")
	}
	maxRaw := opts.MaxRawBytes
	if maxRaw <= 0 || maxRaw > DefaultMaxRawBytes {
		maxRaw = DefaultMaxRawBytes
	}
	watch := opts.WatchInterval
	if watch <= 0 {
		watch = 100 * time.Millisecond
	}
	roots := opts.Roots
	if len(roots) == 0 {
		roots = opts.Resolver.ResolvedRoots()
	}
	return &Service{
		store: opts.Store, manager: opts.Manager, resolver: opts.Resolver,
		roots: append([]string(nil), roots...), provider: opts.BinaryProvider,
		toolAvailable: opts.ToolAvailable, maxRaw: maxRaw, maxTimeout: opts.MaxTimeout, watchInterval: watch,
	}, nil
}

// HasUsableTarget reports whether at least one declared target currently
// resolves under policy. It performs filesystem checks only.
func (s *Service) HasUsableTarget() bool {
	return s != nil && s.resolver != nil && s.resolver.HasUsableTarget()
}

// Run executes one profiler and returns its normalized report. A structurally
// valid but degraded run is returned with complete=false and warnings; invalid
// policy, backend or target input returns a typed error.
func (s *Service) Run(ctx context.Context, tool Tool, req Request) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, ContextError(err)
	}
	if !tool.Valid() {
		return Report{}, Errorf(CodeUnsupported, "unsupported profiler %q", tool)
	}
	if s.toolAvailable != nil {
		ok, reason := s.toolAvailable(tool)
		if !ok {
			if reason == "" {
				reason = "profiler capability is unavailable"
			}
			return Report{}, Errorf(CodeBackendUnavailable, "%s capability is unavailable: %s", tool, reason)
		}
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
		return Report{}, mapTargetError(req.TargetID, err)
	}
	if err := rejectValgrindOptionsFile(resolved.Dir); err != nil {
		return Report{}, err
	}
	programInfo, statErr := os.Stat(resolved.Program)
	programDigest, err := hashProgram(resolved.Program)
	if err != nil {
		return Report{}, Wrap(CodeTargetDenied, err, "hash target %q", resolved.TargetID)
	}
	runDir, err := s.store.Create()
	if err != nil {
		return Report{}, Wrap(CodeQuotaExceeded, err, "create profile run directory")
	}
	rawName := tool.RawArtifactName()
	rawPath := filepath.Join(runDir.Path(), rawName)
	args, err := profileArguments(tool, rawPath, resolved.Program, resolved.Args)
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return Report{}, err
	}
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID:   resolved.TargetID,
		Tool:       "profile_" + string(tool),
		Executable: binary,
		Args:       args,
		Dir:        resolved.Dir,
		Env:        policy.Environment(""),
		Timeout:    timeout,
	})
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return Report{}, Wrap(CodeQuotaExceeded, err, "start %s", tool)
	}

	var rawExceeded atomic.Bool
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		s.watchRaw(rawPath, runDir, job, &rawExceeded, stopWatch)
	}()

	snapshot, waitErr := job.Wait(ctx)
	close(stopWatch)
	<-watchDone
	if waitErr != nil {
		// The caller's context does not own the job lifetime. Cancel the
		// instrumented process group, but do not hang forever if it ignores
		// the kill; the manager's own timeout remains authoritative.
		job.Cancel()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
		snapshot, _ = job.Wait(drainCtx)
		drainCancel()
	}
	adopted, adoptErr := runDir.Adopt(rawName, s.maxRaw)
	var parseErr error
	var callgrind *CallgrindResult
	var massif *MassifResult
	var digest string
	if adoptErr == nil {
		file, openErr := runDir.OpenRead(rawName)
		if openErr != nil {
			parseErr = openErr
		} else {
			hasher := sha256.New()
			reader := io.TeeReader(file, hasher)
			if tool == ToolCallgrind {
				callgrind, parseErr = ParseCallgrind(reader)
			} else {
				massif, parseErr = ParseMassif(reader)
			}
			_, _ = io.Copy(hasher, file)
			_ = file.Close()
			digest = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
		}
	}
	report := s.buildReport(tool, runDir, resolved, binary, version, programDigest, snapshot,
		adopted, adoptErr, rawExceeded.Load(), parseErr, callgrind, massif, digest)
	if statErr == nil && programFileChanged(resolved.Program, programInfo) {
		report.Complete = false
		report.Warnings = append(report.Warnings,
			"target binary changed during profiling; program_sha256 may not match the executed code")
	}
	if writeErr := writeReport(runDir, &report); writeErr != nil {
		return report, Wrap(CodeRunFailed, writeErr, "write normalized profile")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return report, ContextError(ctxErr)
	}
	return report, nil
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
		return 0, Errorf(CodeInvalidArgument, "timeout_ms %d exceeds the configured maximum of %d ms",
			timeoutMS, s.maxTimeout.Milliseconds())
	}
	return requested, nil
}

func (s *Service) resolveBackend() (string, string, error) {
	binary, version, err := s.provider()
	if err != nil {
		return "", "", Wrap(CodeBackendUnavailable, err, "Valgrind is unavailable")
	}
	if !filepath.IsAbs(binary) {
		return "", "", Errorf(CodeBackendUnavailable, "Valgrind path is not absolute")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", Errorf(CodeBackendUnavailable, "Valgrind executable is not usable")
	}
	return binary, version, nil
}

func (s *Service) watchRaw(path string, runDir *artifacts.RunDir, job *jobs.Job, exceeded *atomic.Bool, stop <-chan struct{}) {
	ticker := time.NewTicker(s.watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			limit := s.maxRaw
			if remaining := s.store.MaxBytesPerRun() - runDir.Written(); remaining < limit {
				limit = remaining
			}
			if limit < 0 || info.Size() > limit {
				exceeded.Store(true)
				job.Cancel()
				return
			}
		}
	}
}

func (s *Service) buildReport(tool Tool, runDir *artifacts.RunDir, target policy.Resolved,
	binary, version, programDigest string, snapshot jobs.Snapshot, adopted artifacts.AdoptedFile,
	adoptErr error, rawExceeded bool, parseErr error,
	callgrind *CallgrindResult, massif *MassifResult, digest string) Report {
	report := Report{
		SchemaVersion: APISchemaVersion,
		RunID:         runDir.ID(), Tool: tool, TargetID: target.TargetID, Program: target.Program,
		ProgramSHA256: programDigest,
		Args:          append([]string(nil), target.Args...), ToolPath: binary, ToolVersion: version,
		MetricUnit: tool.metricsUnit(), Metrics: map[string]float64{},
		Artifacts: []Artifact{}, Warnings: []string{},
		ReportArtifact: ReportArtifactName,
	}
	if snapshot.Result != nil {
		report.ExitCode = snapshot.Result.ExitCode
		report.Signal = snapshot.Result.Signal
		report.TimedOut = snapshot.Result.TimedOut
		report.Canceled = snapshot.Result.Canceled
		report.DurationMS = snapshot.Result.DurationMS
	}
	if adopted.Name != "" {
		report.Artifacts = append(report.Artifacts, Artifact{
			Name: adopted.Name, Kind: "raw_profile", Bytes: adopted.Bytes,
			Truncated: adopted.Truncated || rawExceeded, SHA256: digest,
		})
	}
	if adoptErr != nil {
		report.Warnings = append(report.Warnings, "raw profile unavailable: "+adoptErr.Error())
	}
	if rawExceeded {
		report.Warnings = append(report.Warnings, "raw profile exceeded the collection bound")
	}
	if parseErr != nil {
		report.Warnings = append(report.Warnings, "profile parse failed: "+parseErr.Error())
	}
	if snapshot.Error != "" {
		report.Warnings = append(report.Warnings, "job error: "+snapshot.Error)
	}
	complete := snapshot.State == jobs.StateCompleted && snapshot.Result != nil &&
		snapshot.Result.Started && !snapshot.Result.TimedOut && !snapshot.Result.Canceled &&
		adoptErr == nil && parseErr == nil && !rawExceeded &&
		(adopted.Name == "" || !adopted.Truncated) &&
		snapshot.Result.ExitCode == 0 && snapshot.Result.Signal == 0
	if snapshot.Result == nil {
		report.Warnings = append(report.Warnings, "program result is missing")
	}
	if snapshot.Result != nil && snapshot.Result.ExitCode != 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("target exited with %d; profile is preserved but not complete", snapshot.Result.ExitCode))
	}
	if snapshot.Result != nil && snapshot.Result.Signal != 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("target terminated by signal %d; profile may be partial", snapshot.Result.Signal))
	}
	if snapshot.Result != nil && (snapshot.Result.StdoutTruncated || snapshot.Result.StderrTruncated) {
		report.Warnings = append(report.Warnings, "instrumented output exceeded its collection bound")
		complete = false
	}
	if callgrind != nil {
		if len(callgrind.Events) > 0 {
			report.PrimaryMetric = "event_" + sanitizeMetricName(callgrind.Events[0])
		}
		report.Events = formatEventMetrics(callgrind.Events, callgrind.Totals)
		report.TopFunctions = callgrind.TopFunctions
		report.Metrics = callgrindMetrics(callgrind)
		if len(callgrind.Events) == 0 || callgrind.FunctionCount == 0 {
			report.Warnings = append(report.Warnings, "Callgrind profile contains no function evidence")
			complete = false
		}
	}
	if massif != nil {
		report.PrimaryMetric = "heap_peak_bytes"
		report.TimeUnit = massif.TimeUnit
		if len(massif.Warnings) > 0 {
			report.Warnings = append(report.Warnings, massif.Warnings...)
			complete = false
		}
		report.PeakHeapBytes = massif.PeakHeap
		report.PeakHeapExtraBytes = massif.PeakExtra
		report.PeakStacksBytes = massif.PeakStacks
		report.Snapshots = massif.Snapshots
		report.SnapshotSamples = massif.Samples
		report.Metrics = massifMetrics(massif)
		if massif.Snapshots == 0 {
			complete = false
		}
	}
	report.Complete = complete
	return report
}

func trimIncompleteUTF8(data []byte) []byte {
	for range 3 {
		if len(data) == 0 || utf8.Valid(data) {
			return data
		}
		r, size := utf8.DecodeLastRune(data)
		if r != utf8.RuneError || size != 1 {
			return data
		}
		data = data[:len(data)-1]
	}
	return data
}

func rejectValgrindOptionsFile(dir string) error {
	path := filepath.Join(dir, ".valgrindrc")
	if _, err := os.Lstat(path); err == nil {
		return Errorf(CodeTargetDenied,
			"target working directory contains .valgrindrc; refusing untrusted Valgrind options")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Wrap(CodeTargetDenied, err, "inspect target working directory")
	}
	return nil
}

func programFileChanged(path string, before os.FileInfo) bool {
	after, err := os.Stat(path)
	if err != nil || before == nil {
		return true
	}
	return !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime())
}

func hashProgram(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func profileArguments(tool Tool, outPath, program string, programArgs []string) ([]string, error) {
	if !filepath.IsAbs(program) || strings.ContainsRune(program, 0) {
		return nil, Errorf(CodeInvalidArgument, "target program must be absolute")
	}
	if !filepath.IsAbs(outPath) || strings.ContainsRune(outPath, 0) {
		return nil, Errorf(CodeInvalidArgument, "profile output path must be absolute")
	}
	var args []string
	switch tool {
	case ToolCallgrind:
		args = []string{
			"--tool=callgrind",
			"--callgrind-out-file=" + outPath,
			"--collect-jumps=no",
			"--cache-sim=no",
			"--branch-sim=no",
			"--instr-atstart=yes",
			"--collect-atstart=yes",
		}
	case ToolMassif:
		args = []string{
			"--tool=massif",
			"--massif-out-file=" + outPath,
			"--time-unit=i",
			"--detailed-freq=1",
			"--max-snapshots=100",
			"--depth=30",
			"--pages-as-heap=no",
			"--stacks=no",
		}
	default:
		return nil, Errorf(CodeUnsupported, "unsupported profiler %q", tool)
	}
	args = append(args, "--", program)
	args = append(args, programArgs...)
	return args, nil
}

func writeReport(runDir *artifacts.RunDir, report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > DefaultMaxReportBytes {
		return Errorf(CodeQuotaExceeded, "normalized profile exceeds %d bytes", DefaultMaxReportBytes)
	}
	return runDir.WriteFileAtomic(ReportArtifactName, data)
}

func mapTargetError(targetID string, err error) error {
	var denied *policy.DeniedError
	if errors.As(err, &denied) {
		if denied.Reason == policy.ReasonUnknownTarget {
			return Wrap(CodeTargetUnknown, err, "target %q is not declared", targetID)
		}
		return Wrap(CodeTargetDenied, err, "target %q is denied by policy", targetID)
	}
	return Wrap(CodeTargetDenied, err, "resolve target %q", targetID)
}

// Load reads a persisted normalized profile report.
func (s *Service) Load(ctx context.Context, runID string) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, ContextError(err)
	}
	if !s.store.RunExists(runID) {
		return Report{}, Errorf(CodeProfileUnavailable, "profile run %q does not exist", runID)
	}
	file, err := s.store.OpenArtifact(runID, ReportArtifactName)
	if err != nil {
		return Report{}, Wrap(CodeProfileUnavailable, err, "open normalized profile")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, DefaultMaxReportBytes+1))
	if err != nil {
		return Report{}, Wrap(CodeProfileUnavailable, err, "read normalized profile")
	}
	if len(data) > DefaultMaxReportBytes {
		return Report{}, Errorf(CodeQuotaExceeded, "normalized profile exceeds its bound")
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, Wrap(CodeProfileUnavailable, err, "decode normalized profile")
	}
	if report.SchemaVersion != APISchemaVersion || !report.Tool.Valid() || report.RunID != runID {
		return Report{}, Errorf(CodeProfileUnavailable, "normalized profile identity is invalid")
	}
	return report, nil
}

// Compare reads two persisted profiles and reports descriptive deltas. It
// refuses profiles whose tool, Callgrind events or Massif time unit differ.
func (s *Service) Compare(ctx context.Context, baseRunID, targetRunID string) (Comparison, error) {
	base, err := s.Load(ctx, baseRunID)
	if err != nil {
		return Comparison{}, err
	}
	target, err := s.Load(ctx, targetRunID)
	if err != nil {
		return Comparison{}, err
	}
	if !base.Complete || !target.Complete {
		return Comparison{}, Errorf(CodeProfileIncomplete,
			"both profiles must be complete before comparison")
	}
	if base.Tool != target.Tool {
		return Comparison{}, Errorf(CodeIncompatibleProfiles,
			"profiles use different tools: %s and %s", base.Tool, target.Tool)
	}
	if base.MetricUnit != target.MetricUnit {
		return Comparison{}, Errorf(CodeIncompatibleProfiles,
			"profiles use different normalized units: %q and %q", base.MetricUnit, target.MetricUnit)
	}
	var reasons []string
	switch base.Tool {
	case ToolCallgrind:
		if !sameStrings(eventNames(base.Events), eventNames(target.Events)) {
			reasons = append(reasons, "Callgrind event sets/order differ")
		}
	case ToolMassif:
		if base.TimeUnit != target.TimeUnit {
			reasons = append(reasons, "Massif time units differ: "+base.TimeUnit+" and "+target.TimeUnit)
		}
	}
	if len(reasons) > 0 {
		return Comparison{}, Wrap(CodeIncompatibleProfiles,
			fmt.Errorf("%s", strings.Join(reasons, "; ")), "profiles are not comparable")
	}
	deltas := make(map[string]float64, len(base.Metrics))
	percent := make(map[string]float64)
	for key, baseValue := range base.Metrics {
		if key == "functions" || key == "snapshots" {
			continue
		}
		targetValue, ok := target.Metrics[key]
		if !ok {
			continue
		}
		delta := targetValue - baseValue
		deltas[key] = delta
		if baseValue != 0 {
			percent[key] = delta / baseValue * 100
		}
	}
	warnings := []string{
		"deltas are descriptive and do not establish statistical significance or a performance regression",
		"callgrind instruction/count profiles and massif heap profiles are deterministic only within the same binary, input and tool configuration",
	}
	if base.ProgramSHA256 != "" && target.ProgramSHA256 != "" && base.ProgramSHA256 != target.ProgramSHA256 {
		warnings = append(warnings, "the two profiles use different executable hashes; deltas are not like-for-like")
	}
	if base.ToolVersion != target.ToolVersion {
		warnings = append(warnings, "the two profiles use different Valgrind versions")
	}
	if base.TargetID != target.TargetID || !sameStrings(base.Args, target.Args) {
		warnings = append(warnings, "the two profiles use different target ids or arguments")
	}
	return Comparison{
		SchemaVersion: APISchemaVersion, BaseRunID: baseRunID, TargetRunID: targetRunID,
		Tool: base.Tool, MetricUnit: base.MetricUnit, Compatible: true,
		BaseMetrics: base.Metrics, TargetMetrics: target.Metrics,
		Deltas: deltas, Percent: percent,
		Warnings: warnings,
	}, nil
}

func eventNames(events []EventMetric) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Artifact reads a bounded excerpt of preserved profile evidence.
func (s *Service) Artifact(ctx context.Context, req ArtifactRequest) (ArtifactOutput, error) {
	if ctx == nil {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, ContextError(err)
	}
	report, err := s.Load(ctx, req.RunID)
	if err != nil {
		return ArtifactOutput{}, err
	}
	allowed := map[string]bool{ReportArtifactName: true}
	for _, artifact := range report.Artifacts {
		allowed[artifact.Name] = true
	}
	if req.FileName == "" || !allowed[req.FileName] {
		return ArtifactOutput{}, Errorf(CodeArtifactNotFound, "artifact %q is not part of this profile", req.FileName)
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
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "open profile artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "inspect profile artifact")
	}
	if req.Offset > info.Size() {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "offset exceeds the artifact")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, req.Offset, int64(maxBytes)))
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactNotFound, err, "read profile artifact")
	}
	truncatedUTF8 := false
	if req.Offset+int64(len(data)) < info.Size() {
		raw := data
		data = trimIncompleteUTF8(data)
		if len(data) == 0 && len(raw) > 0 {
			data = raw[:1]
		}
		if len(data) != len(raw) || !utf8.Valid(data) {
			truncatedUTF8 = true
		}
	}
	return ArtifactOutput{
		SchemaVersion: APISchemaVersion, RunID: req.RunID, FileName: req.FileName,
		Bytes: info.Size(), Offset: req.Offset, ReturnedBytes: len(data),
		MaxBytes: maxBytes, HasMore: req.Offset+int64(len(data)) < info.Size(),
		NextOffset: req.Offset + int64(len(data)),
		Content:    strings.ToValidUTF8(string(data), "\uFFFD"), TruncatedUTF8: truncatedUTF8,
	}, nil
}
