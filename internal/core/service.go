// Package core analyzes configured core dumps with a fixed GDB batch command.
// It never accepts a client path, debugger command or shell fragment.
package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

const (
	// SchemaVersion versions core API payloads.
	SchemaVersion = 1
	// DefaultTimeout bounds one GDB core inspection.
	DefaultTimeout = 30 * time.Second
	// MaxOutputBytes bounds the raw GDB output retained in the response.
	MaxOutputBytes = 4 << 20
	// MaxReportOutputBytes bounds the output text exposed to MCP.
	MaxReportOutputBytes = 64 << 10
)

// ErrorCode is a stable core API error code.
type ErrorCode string

const (
	CodeInvalidArgument    ErrorCode = "invalid_argument"
	CodeProfileUnknown     ErrorCode = "profile_unknown"
	CodeProfileUnavailable ErrorCode = "profile_unavailable"
	CodeBackendUnavailable ErrorCode = "backend_unavailable"
	CodeToolDisabled       ErrorCode = "tool_disabled"
	CodeTimeout            ErrorCode = "timeout"
	CodeCanceled           ErrorCode = "canceled"
	CodeToolError          ErrorCode = "tool_error"
	CodeInternal           ErrorCode = "internal"
)

// Error is a typed core API error.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "core: " + e.Message + ": " + e.Err.Error()
	}
	return "core: " + e.Message
}
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a typed core error.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a typed core error around a cause.
func Wrap(code ErrorCode, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the stable error code.
func CodeOf(err error) ErrorCode {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// CommandRunner executes one bounded command; *runner.Runner implements it.
type CommandRunner interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}

// BinaryProvider revalidates GDB at invocation time.
type BinaryProvider func() (path, version string, err error)

// ServiceOptions wires core analysis to configuration and GDB.
type ServiceOptions struct {
	Runner   CommandRunner
	Binary   BinaryProvider
	Profiles []config.CoreProfile
	Roots    []string
	Timeout  time.Duration
	Store    *artifacts.Store
}

// Service analyzes configured core dumps.
type Service struct {
	runner   CommandRunner
	binary   BinaryProvider
	profiles map[string]config.CoreProfile
	roots    []string
	timeout  time.Duration
	store    *artifacts.Store
	sem      chan struct{}
}

// NewService builds a core inspection service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Runner == nil {
		return nil, errors.New("core: nil command runner")
	}
	if opts.Binary == nil {
		return nil, errors.New("core: nil binary provider")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	profiles := make(map[string]config.CoreProfile, len(opts.Profiles))
	for _, profile := range opts.Profiles {
		profiles[profile.ID] = profile
	}
	return &Service{
		runner: opts.Runner, binary: opts.Binary, profiles: profiles,
		roots: append([]string(nil), opts.Roots...), timeout: opts.Timeout,
		store: opts.Store, sem: make(chan struct{}, 1),
	}, nil
}

// HasProfiles reports whether at least one core profile is declared.
func (s *Service) HasProfiles() bool { return len(s.profiles) > 0 }

// Request selects one configured core profile.
type Request struct {
	ProfileID string `json:"profile_id" jsonschema:"declared core profile id; never a filesystem path"`
}

// Thread is one GDB-reported thread line, bounded.
type Thread struct {
	ID     int    `json:"id"`
	Line   string `json:"line"`
	Active bool   `json:"active,omitempty"`
}

// Evidence references the preserved raw GDB output.
type Evidence struct {
	RunID     string `json:"run_id"`
	Artifact  string `json:"artifact"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Report is the bounded result of one core inspection.
type Report struct {
	SchemaVersion  int       `json:"schema_version"`
	ProfileID      string    `json:"profile_id"`
	Executable     string    `json:"executable"`
	Core           string    `json:"core"`
	Backend        string    `json:"backend"`
	BackendVersion string    `json:"backend_version,omitempty"`
	Complete       bool      `json:"complete"`
	ExitCode       int       `json:"exit_code"`
	TimedOut       bool      `json:"timed_out"`
	Output         string    `json:"output"`
	Threads        []Thread  `json:"threads"`
	Evidence       *Evidence `json:"evidence,omitempty"`
	Warnings       []string  `json:"warnings"`
}

var threadLine = regexp.MustCompile(`(?m)^\s*(\*?)\s*(\d+)\s+Thread`)

// Analyze opens one configured core with fixed GDB batch commands.
func (s *Service) Analyze(ctx context.Context, req Request) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, mapContextError(err)
	}
	if strings.TrimSpace(req.ProfileID) == "" {
		return Report{}, Errorf(CodeInvalidArgument, "profile_id must not be empty")
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return Report{}, mapContextError(ctx.Err())
	}
	profile, ok := s.profiles[req.ProfileID]
	if !ok {
		return Report{}, Errorf(CodeProfileUnknown, "core profile %q is not declared", req.ProfileID)
	}
	executable, corePath, executableBefore, coreBefore, err := s.resolveProfile(profile)
	if err != nil {
		return Report{}, err
	}
	binary, version, err := s.binary()
	if err != nil {
		return Report{}, Wrap(CodeBackendUnavailable, err, "GDB backend is unavailable")
	}
	if !filepath.IsAbs(binary) {
		return Report{}, Errorf(CodeBackendUnavailable, "GDB executable is not absolute")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o111 == 0 {
		return Report{}, Errorf(CodeBackendUnavailable, "GDB executable is not usable")
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	result, runErr := s.runner.Run(ctx, runner.Request{
		Executable: binary,
		Args: []string{
			"--quiet", "--nx", "--nh", "--batch",
			"-iex", "set pagination off",
			"-iex", "set confirm off",
			"-iex", "set debuginfod enabled off",
			"-iex", "set auto-load off",
			"-iex", "set style enabled off",
			"-ex", "set print frame-arguments none",
			"-ex", "set print elements 0",
			"-ex", "set history save off",
			"-ex", "info threads",
			"-ex", "thread apply all bt 32",
			"-c", corePath,
			executable,
		},
		Dir:            filepath.Dir(executable),
		Env:            policy.Environment(""),
		Timeout:        s.timeout,
		MaxOutputBytes: MaxOutputBytes,
		Stdout:         stdout,
		Stderr:         stderr,
	})
	if runErr != nil {
		return Report{}, Wrap(CodeToolError, runErr, "run GDB core inspection")
	}
	changed := fileIdentityChanged(executable, executableBefore) || fileIdentityChanged(corePath, coreBefore)
	report := Report{
		SchemaVersion:  SchemaVersion,
		ProfileID:      profile.ID,
		Executable:     executable,
		Core:           corePath,
		Backend:        "gdb",
		BackendVersion: version,
		ExitCode:       result.ExitCode,
		TimedOut:       result.TimedOut,
		Threads:        []Thread{},
		Warnings:       []string{},
	}
	combined := stdout.String()
	if stderr.Len() > 0 {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr.String()
	}
	rawOutput := combined
	outputTruncated := len(combined) > MaxReportOutputBytes
	if outputTruncated {
		combined = combined[:MaxReportOutputBytes]
		report.Warnings = append(report.Warnings, "GDB output was truncated to the response bound")
	}
	report.Output = combined
	evidence, evidenceErr := s.preserveEvidence(rawOutput)
	if evidence != nil {
		evidence.Truncated = outputTruncated || result.StdoutTruncated || result.StderrTruncated
	}
	if evidenceErr != nil {
		report.Warnings = append(report.Warnings, "raw GDB output was not preserved: "+evidenceErr.Error())
	} else {
		report.Evidence = evidence
	}
	threads, threadsTruncated := parseThreads(stdout.String())
	report.Threads = threads
	if threadsTruncated {
		report.Warnings = append(report.Warnings, "thread list was truncated at the response bound")
		report.Complete = false
	}
	completeOutput := !outputTruncated
	report.Complete = result.Started && result.ExitCode == 0 && !result.TimedOut &&
		!result.Canceled && !result.StdoutTruncated && !result.StderrTruncated &&
		completeOutput && !threadsTruncated && evidenceErr == nil
	if result.TimedOut {
		report.Warnings = append(report.Warnings, "GDB core inspection timed out")
	}
	if result.Canceled {
		report.Warnings = append(report.Warnings, "GDB core inspection was canceled")
	}
	if result.StdoutTruncated || result.StderrTruncated {
		report.Warnings = append(report.Warnings, "GDB output exceeded the collection bound")
	}
	if result.ExitCode != 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("GDB exited with %d", result.ExitCode))
	}
	lowerOutput := strings.ToLower(combined)
	if !strings.Contains(lowerOutput, "core was generated") {
		report.Warnings = append(report.Warnings, "GDB output did not confirm a generated core; treat the report as incomplete")
		report.Complete = false
	}
	for _, marker := range []string{
		"core file may not match specified executable",
		"Cannot access memory",
		"Could not load shared library symbols",
		"No debugging symbols found",
		"Backtrace stopped",
		"file-backed mapping",
	} {
		if strings.Contains(lowerOutput, strings.ToLower(marker)) {
			report.Warnings = append(report.Warnings, "GDB reported degraded core evidence: "+marker)
			report.Complete = false
		}
	}
	if strings.Contains(combined, "More stack frames follow") {
		report.Warnings = append(report.Warnings, "backtrace was depth-limited; deeper frames were not examined")
		report.Complete = false
	}
	if len(report.Threads) == 0 {
		report.Warnings = append(report.Warnings, "no threads were parsed from the core")
		report.Complete = false
	}
	if changed {
		report.Warnings = append(report.Warnings, "core or executable identity changed during inspection")
		report.Complete = false
	}
	return report, nil
}

func (s *Service) preserveEvidence(output string) (*Evidence, error) {
	if s.store == nil {
		return nil, nil
	}
	runDir, err := s.store.Create()
	if err != nil {
		return nil, err
	}
	const artifactName = "core-gdb.log"
	if err := runDir.WriteFileAtomic(artifactName, []byte(output)); err != nil {
		_ = s.store.Remove(runDir.ID())
		return nil, err
	}
	return &Evidence{RunID: runDir.ID(), Artifact: artifactName, Bytes: int64(len(output))}, nil
}

// ArtifactRequest selects a bounded excerpt of preserved core evidence.
type ArtifactRequest struct {
	RunID    string `json:"run_id" jsonschema:"run id from core_analyze evidence"`
	Offset   int64  `json:"offset,omitempty" jsonschema:"byte offset"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum excerpt bytes"`
}

// ArtifactOutput is a bounded UTF-8 excerpt of preserved core evidence.
type ArtifactOutput struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	FileName      string `json:"file_name"`
	Bytes         int64  `json:"bytes"`
	Offset        int64  `json:"offset"`
	ReturnedBytes int    `json:"returned_bytes"`
	MaxBytes      int    `json:"max_bytes"`
	HasMore       bool   `json:"has_more"`
	NextOffset    int64  `json:"next_offset"`
	Content       string `json:"content"`
}

// Artifact reads a bounded excerpt of the preserved raw GDB output.
func (s *Service) Artifact(ctx context.Context, req ArtifactRequest) (ArtifactOutput, error) {
	if ctx == nil || s.store == nil {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "artifact access is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, mapContextError(err)
	}
	if req.Offset < 0 || req.MaxBytes < 0 {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "offset and max_bytes must not be negative")
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = 64 << 10
	}
	if maxBytes > 1<<20 {
		maxBytes = 1 << 20
	}
	const name = "core-gdb.log"
	file, err := s.store.OpenArtifact(req.RunID, name)
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeProfileUnavailable, err, "core evidence is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeProfileUnavailable, err, "inspect core evidence")
	}
	if req.Offset > info.Size() {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "offset exceeds the artifact")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, req.Offset, int64(maxBytes)))
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeProfileUnavailable, err, "read core evidence")
	}
	if req.Offset+int64(len(data)) < info.Size() {
		raw := data
		data = trimCoreRune(data)
		if len(data) == 0 && len(raw) > 0 {
			data = raw[:1]
		}
	}
	return ArtifactOutput{
		SchemaVersion: SchemaVersion, RunID: req.RunID, FileName: name,
		Bytes: info.Size(), Offset: req.Offset, ReturnedBytes: len(data),
		MaxBytes: maxBytes, HasMore: req.Offset+int64(len(data)) < info.Size(),
		NextOffset: req.Offset + int64(len(data)), Content: strings.ToValidUTF8(string(data), "\uFFFD"),
	}, nil
}

func trimCoreRune(data []byte) []byte {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return data
}

func (s *Service) resolveProfile(profile config.CoreProfile) (string, string, os.FileInfo, os.FileInfo, error) {
	executable, err := filepath.EvalSymlinks(profile.Executable)
	if err != nil {
		return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q executable is unavailable", profile.ID)
	}
	corePath, err := filepath.EvalSymlinks(profile.Core)
	if err != nil {
		return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q core file is unavailable", profile.ID)
	}
	if len(s.roots) > 0 {
		if !withinAny(s.roots, executable) || !withinAny(s.roots, corePath) {
			return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q resolves outside workspace roots", profile.ID)
		}
	}
	exeInfo, err := os.Stat(executable)
	if err != nil || !exeInfo.Mode().IsRegular() {
		return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q executable is not a regular file", profile.ID)
	}
	coreInfo, err := os.Stat(corePath)
	if err != nil || !coreInfo.Mode().IsRegular() {
		return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q core is not a regular file", profile.ID)
	}
	if !trustedFile(exeInfo) || !trustedFile(coreInfo) {
		return "", "", nil, nil, Errorf(CodeProfileUnavailable, "core profile %q files are not owned by this user or are group/world writable", profile.ID)
	}
	return executable, corePath, exeInfo, coreInfo, nil
}

func trustedFile(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return false
	}
	return info.Mode().Perm()&0o022 == 0
}

func fileIdentityChanged(path string, before os.FileInfo) bool {
	after, err := os.Stat(path)
	if err != nil {
		return true
	}
	if stat, ok := after.Sys().(*syscall.Stat_t); ok {
		beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
		if !beforeOK || stat.Dev != beforeStat.Dev || stat.Ino != beforeStat.Ino ||
			stat.Uid != beforeStat.Uid || stat.Gid != beforeStat.Gid {
			return true
		}
	}
	return before.Size() != after.Size() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime())
}

func withinAny(roots []string, path string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

func parseThreads(output string) ([]Thread, bool) {
	threads := make([]Thread, 0)
	lines := strings.Split(output, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		match := threadLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if len(threads) >= 256 {
			return threads, true
		}
		id, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		text := line
		if len(text) > 256 {
			text = text[:256]
		}
		threads = append(threads, Thread{ID: id, Line: text, Active: match[1] == "*"})
	}
	return threads, false
}

func mapContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "core inspection canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "core inspection timed out")
	}
	return Wrap(CodeInternal, err, "core inspection failed")
}
