// Package config loads and validates the server configuration.
//
// Configuration files are strict JSON: unknown fields are rejected, the schema
// version must match, and all paths are validated lexically before use. Path
// existence is intentionally not required here; doctor reports it without
// executing anything, and runtime components revalidate before use.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SchemaVersion is the only configuration schema understood by this build.
const SchemaVersion = 1

// File size and cardinality limits keep parsing bounded.
const (
	MaxFileBytes      = 1 << 20
	MaxWorkspaceRoots = 32
	MaxTargets        = 128
	MaxAllowedArgs    = 64
	MaxArgBytes       = 4096
	MaxPathBytes      = 4096

	MaxSuppressionProfiles  = 32
	MaxSuppressionFileBytes = 1 << 20
	MaxSanitizersPerTarget  = 4
)

// Defaults applied when a limit is omitted (zero).
const (
	DefaultTimeoutSeconds   = 120
	DefaultMaxJobs          = 2
	DefaultMaxSessions      = 2
	DefaultMaxResponseItems = 100
	DefaultMaxReportBytes   = 10 << 20
)

// Accepted ranges for limits.
const (
	MaxTimeoutSeconds = 86400
	MaxMaxJobs        = 32
	MaxMaxSessions    = 32
	MinMaxReportBytes = 1024
	MaxMaxReportBytes = 1 << 30
	MaxResponseItems  = 10000
)

const (
	ProfileSafeDefault   = "safe-default"
	ProfileTrustedLocal  = "trusted-local"
	ProfileElevatedLocal = "elevated-local"
)

var targetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidSanitizer reports whether a target sanitizer declaration is supported.
func ValidSanitizer(name string) bool {
	switch name {
	case "asan", "ubsan", "lsan", "tsan":
		return true
	default:
		return false
	}
}

// Config is the root of the configuration document.
type Config struct {
	SchemaVersion  int      `json:"schema_version"`
	WorkspaceRoots []string `json:"workspace_roots"`
	ReportDir      string   `json:"report_dir"`
	AllowedTargets []Target `json:"allowed_targets"`
	Backends       Backends `json:"backends"`
	Policy         Policy   `json:"policy"`
	Limits         Limits   `json:"limits"`
	Valgrind       Valgrind `json:"valgrind"`
}

// Valgrind holds optional Valgrind-specific configuration.
type Valgrind struct {
	// Suppressions maps an administrator-defined profile id to a suppression
	// file. The file is resolved and revalidated at invocation time.
	Suppressions map[string]string `json:"suppressions,omitempty"`
}

// Target is an executable the server is allowed to run on request.
type Target struct {
	ID          string   `json:"id"`
	Program     string   `json:"program"`
	Cwd         string   `json:"cwd"`
	AllowedArgs []string `json:"allowed_args"`
	// Sanitizers declares the sanitizer runtimes the target binary was built
	// with. The server does not build or instrument targets; this is an
	// administrator trust declaration used to validate the per-sanitizer run
	// tools.
	Sanitizers []string `json:"sanitizers,omitempty"`
}

// Backends records which external backends are enabled by configuration.
type Backends struct {
	Valgrind   bool `json:"valgrind"`
	GDB        bool `json:"gdb"`
	LLDB       bool `json:"lldb"`
	Sanitizers bool `json:"sanitizers"`
}

// Policy records the trust profile and explicit elevated permissions.
type Policy struct {
	Profile  string `json:"profile"`
	Attach   bool   `json:"attach"`
	Evaluate bool   `json:"evaluate"`
}

// Limits bounds execution and response sizes.
type Limits struct {
	TimeoutSeconds   int   `json:"timeout_seconds"`
	MaxJobs          int   `json:"max_jobs"`
	MaxSessions      int   `json:"max_sessions"`
	MaxReportBytes   int64 `json:"max_report_bytes"`
	MaxResponseItems int   `json:"max_response_items"`
}

// Load reads, parses and validates one configuration file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	if !info.Mode().IsRegular() {
		return nil, &LoadError{Path: path, Err: errors.New("not a regular file")}
	}
	if info.Size() > MaxFileBytes {
		return nil, &LoadError{
			Path: path,
			Err:  fmt.Errorf("file exceeds %d bytes", MaxFileBytes),
		}
	}

	return Parse(f)
}

// Parse decodes and validates a configuration document.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return nil, &ParseError{Reason: "read: " + err.Error()}
	}
	if len(data) > MaxFileBytes {
		return nil, &ParseError{Reason: fmt.Sprintf("file exceeds %d bytes", MaxFileBytes)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &ParseError{Reason: "empty file"}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, &ParseError{Reason: err.Error()}
	}

	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, &ParseError{Reason: "unexpected trailing JSON value"}
		}
		return nil, &ParseError{Reason: "trailing data: " + err.Error()}
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the configuration and normalizes paths in place.
func (c *Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fieldError("schema_version", "must be %d", SchemaVersion)
	}
	if len(c.WorkspaceRoots) == 0 {
		return fieldError("workspace_roots", "at least one root is required")
	}
	if len(c.WorkspaceRoots) > MaxWorkspaceRoots {
		return fieldError("workspace_roots", "at most %d roots are allowed", MaxWorkspaceRoots)
	}

	roots := make([]string, len(c.WorkspaceRoots))
	seenRoots := make(map[string]struct{}, len(c.WorkspaceRoots))
	for i, raw := range c.WorkspaceRoots {
		field := fmt.Sprintf("workspace_roots[%d]", i)
		root, err := normalizePath(field, raw)
		if err != nil {
			return err
		}
		if err := rejectSensitivePath(field, root); err != nil {
			return err
		}
		if _, duplicate := seenRoots[root]; duplicate {
			return fieldError(field, "duplicate root %q", root)
		}
		seenRoots[root] = struct{}{}
		roots[i] = root
		c.WorkspaceRoots[i] = root
	}

	reportDir, err := normalizePath("report_dir", c.ReportDir)
	if err != nil {
		return err
	}
	if err := rejectSensitivePath("report_dir", reportDir); err != nil {
		return err
	}
	c.ReportDir = reportDir

	if len(c.AllowedTargets) > MaxTargets {
		return fieldError("allowed_targets", "at most %d targets are allowed", MaxTargets)
	}

	seenIDs := make(map[string]struct{}, len(c.AllowedTargets))
	for i := range c.AllowedTargets {
		target := &c.AllowedTargets[i]
		field := fmt.Sprintf("allowed_targets[%d]", i)

		if !targetIDPattern.MatchString(target.ID) {
			return fieldError(field+".id", "must match %s", targetIDPattern)
		}
		if _, duplicate := seenIDs[target.ID]; duplicate {
			return fieldError(field+".id", "duplicate target id %q", target.ID)
		}
		seenIDs[target.ID] = struct{}{}

		program, err := normalizePath(field+".program", target.Program)
		if err != nil {
			return err
		}
		if err := rejectSensitivePath(field+".program", program); err != nil {
			return err
		}
		if !pathWithinAny(roots, program) {
			return fieldError(field+".program", "must be inside workspace_roots")
		}

		cwd, err := normalizePath(field+".cwd", target.Cwd)
		if err != nil {
			return err
		}
		if err := rejectSensitivePath(field+".cwd", cwd); err != nil {
			return err
		}
		if !pathWithinAny(roots, cwd) {
			return fieldError(field+".cwd", "must be inside workspace_roots")
		}

		if len(target.AllowedArgs) > MaxAllowedArgs {
			return fieldError(field+".allowed_args", "at most %d arguments are allowed", MaxAllowedArgs)
		}
		if len(target.Sanitizers) > MaxSanitizersPerTarget {
			return fieldError(field+".sanitizers", "at most %d sanitizer declarations are allowed", MaxSanitizersPerTarget)
		}
		seenSanitizers := make(map[string]struct{}, len(target.Sanitizers))
		for j, name := range target.Sanitizers {
			sanitizerField := fmt.Sprintf("%s.sanitizers[%d]", field, j)
			if !ValidSanitizer(name) {
				return fieldError(sanitizerField, "must be one of asan, ubsan, lsan or tsan")
			}
			if _, duplicate := seenSanitizers[name]; duplicate {
				return fieldError(sanitizerField, "duplicate sanitizer %q", name)
			}
			seenSanitizers[name] = struct{}{}
		}

		for j, arg := range target.AllowedArgs {
			argField := fmt.Sprintf("%s.allowed_args[%d]", field, j)
			if strings.ContainsRune(arg, 0) {
				return fieldError(argField, "must not contain NUL")
			}
			if len(arg) > MaxArgBytes {
				return fieldError(argField, "must be at most %d bytes", MaxArgBytes)
			}
		}

		target.Program = program
		target.Cwd = cwd
	}

	switch c.Policy.Profile {
	case ProfileSafeDefault, ProfileTrustedLocal, ProfileElevatedLocal:
	default:
		return fieldError("policy.profile", "must be one of %q, %q, %q",
			ProfileSafeDefault, ProfileTrustedLocal, ProfileElevatedLocal)
	}
	if (c.Policy.Attach || c.Policy.Evaluate) && c.Policy.Profile != ProfileElevatedLocal {
		return fieldError("policy", "attach and evaluate require profile %q", ProfileElevatedLocal)
	}

	if err := c.validateValgrind(roots); err != nil {
		return err
	}
	return c.validateLimits()
}

func (c *Config) validateValgrind(roots []string) error {
	if len(c.Valgrind.Suppressions) > MaxSuppressionProfiles {
		return fieldError("valgrind.suppressions", "at most %d profiles are allowed", MaxSuppressionProfiles)
	}
	if c.Valgrind.Suppressions == nil {
		return nil
	}
	for id, raw := range c.Valgrind.Suppressions {
		field := "valgrind.suppressions[" + id + "]"
		if !targetIDPattern.MatchString(id) {
			return fieldError(field, "profile id must match %s", targetIDPattern)
		}
		path, err := normalizePath(field, raw)
		if err != nil {
			return err
		}
		if err := rejectSensitivePath(field, path); err != nil {
			return err
		}
		if !pathWithinAny(roots, path) {
			return fieldError(field, "must be inside workspace_roots")
		}
		c.Valgrind.Suppressions[id] = path
	}
	return nil
}

func (c *Config) validateLimits() error {
	switch {
	case c.Limits.TimeoutSeconds < 1 || c.Limits.TimeoutSeconds > MaxTimeoutSeconds:
		return fieldError("limits.timeout_seconds", "must be between 1 and %d", MaxTimeoutSeconds)
	case c.Limits.MaxJobs < 1 || c.Limits.MaxJobs > MaxMaxJobs:
		return fieldError("limits.max_jobs", "must be between 1 and %d", MaxMaxJobs)
	case c.Limits.MaxSessions < 1 || c.Limits.MaxSessions > MaxMaxSessions:
		return fieldError("limits.max_sessions", "must be between 1 and %d", MaxMaxSessions)
	case c.Limits.MaxReportBytes < MinMaxReportBytes || c.Limits.MaxReportBytes > MaxMaxReportBytes:
		return fieldError("limits.max_report_bytes", "must be between %d and %d",
			MinMaxReportBytes, MaxMaxReportBytes)
	case c.Limits.MaxResponseItems < 1 || c.Limits.MaxResponseItems > MaxResponseItems:
		return fieldError("limits.max_response_items", "must be between 1 and %d", MaxResponseItems)
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Policy.Profile == "" {
		c.Policy.Profile = ProfileSafeDefault
	}
	if c.Limits.TimeoutSeconds == 0 {
		c.Limits.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if c.Limits.MaxJobs == 0 {
		c.Limits.MaxJobs = DefaultMaxJobs
	}
	if c.Limits.MaxSessions == 0 {
		c.Limits.MaxSessions = DefaultMaxSessions
	}
	if c.Limits.MaxReportBytes == 0 {
		c.Limits.MaxReportBytes = DefaultMaxReportBytes
	}
	if c.Limits.MaxResponseItems == 0 {
		c.Limits.MaxResponseItems = DefaultMaxResponseItems
	}
}

func normalizePath(field, value string) (string, error) {
	if value == "" {
		return "", fieldError(field, "must not be empty")
	}
	if len(value) > MaxPathBytes {
		return "", fieldError(field, "must be at most %d bytes", MaxPathBytes)
	}
	if strings.ContainsRune(value, 0) {
		return "", fieldError(field, "must not contain NUL")
	}
	if !filepath.IsAbs(value) {
		return "", fieldError(field, "must be an absolute path")
	}
	return filepath.Clean(value), nil
}

func rejectSensitivePath(field, path string) error {
	if path == string(filepath.Separator) {
		return fieldError(field, "must not be the filesystem root")
	}
	for _, prefix := range []string{"/proc", "/sys", "/dev"} {
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return fieldError(field, "must not be inside %s", prefix)
		}
	}
	return nil
}

func pathWithinAny(roots []string, path string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func fieldError(field, format string, args ...any) error {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// LoadError reports an unreadable configuration file.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("config: cannot read %q: %v", e.Path, e.Err)
}

// Unwrap exposes the underlying filesystem error.
func (e *LoadError) Unwrap() error { return e.Err }

// ParseError reports malformed JSON or a document that is too large.
type ParseError struct {
	Reason string
}

func (e *ParseError) Error() string { return "config: invalid document: " + e.Reason }

// ValidationError reports a semantically invalid field.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config: %s: %s", e.Field, e.Reason)
}
