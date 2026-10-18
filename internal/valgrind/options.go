package valgrind

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ErrorExitCode is passed to Valgrind so that detected errors are visible in
// the process exit code. It is a marker only: the XML document remains the
// authoritative evidence, and a target may legitimately exit with the same code.
const ErrorExitCode = 42

// ProtocolTool is the Valgrind tool this pipeline parses.
const ProtocolTool = "memcheck"

// LeakCheck selects Valgrind's --leak-check mode.
type LeakCheck string

const (
	LeakCheckNone    LeakCheck = "none"
	LeakCheckSummary LeakCheck = "summary"
	LeakCheckFull    LeakCheck = "full"
)

// ShowLeakKinds selects which leak kinds Valgrind reports.
type ShowLeakKinds string

const (
	ShowLeakKindsAll              ShowLeakKinds = "all"
	ShowLeakKindsDefinite         ShowLeakKinds = "definite"
	ShowLeakKindsPossible         ShowLeakKinds = "possible"
	ShowLeakKindsReachable        ShowLeakKinds = "reachable"
	ShowLeakKindsDefinitePossible ShowLeakKinds = "definite,possible"
)

// Default options mirror the reference Memcheck invocation while keeping the
// call depth bounded.
const (
	DefaultNumCallers = 12
	MinNumCallers     = 1
	MaxNumCallers     = 50
)

// Options is the strict, allowlisted Valgrind command surface. There is no
// free-form flag string and no pass-through of client-supplied options.
type Options struct {
	Tool                Tool
	LeakCheck           LeakCheck
	ShowLeakKinds       ShowLeakKinds
	TrackOrigins        bool
	NumCallers          int
	SuppressionsProfile string
	// TraceChildren is part of the future contract but is rejected here:
	// tracing children produces one XML document per process, and J3 does not
	// implement multi-process collection or attribution yet.
	TraceChildren bool
}

// DefaultOptions returns the Memcheck defaults used when a request omits
// options.
func DefaultOptions() Options {
	return Options{
		Tool:          ToolMemcheck,
		LeakCheck:     LeakCheckFull,
		ShowLeakKinds: ShowLeakKindsAll,
		TrackOrigins:  true,
		NumCallers:    DefaultNumCallers,
	}
}

// OptionError reports an invalid or unsupported option.
type OptionError struct {
	Field  string
	Reason string
}

func (e *OptionError) Error() string {
	return fmt.Sprintf("valgrind: option %s: %s", e.Field, e.Reason)
}

// Validate checks the option set for o.Tool and applies documented defaults.
func (o *Options) Validate() error {
	if o.Tool == "" {
		o.Tool = ToolMemcheck
	}
	return o.ValidateFor(o.Tool)
}

// ValidateFor checks the option set for tool and applies documented defaults.
func (o *Options) ValidateFor(tool Tool) error {
	if !tool.Valid() {
		return &OptionError{Field: "tool", Reason: fmt.Sprintf("unsupported Valgrind tool %q", tool)}
	}
	o.Tool = tool
	if tool != ToolMemcheck {
		if o.NumCallers == 0 {
			o.NumCallers = DefaultNumCallers
		}
		if o.NumCallers < MinNumCallers || o.NumCallers > MaxNumCallers {
			return &OptionError{Field: "num_callers", Reason: fmt.Sprintf("must be between %d and %d",
				MinNumCallers, MaxNumCallers)}
		}
		if o.TraceChildren {
			return &OptionError{Field: "trace_children", Reason: "is not supported (separate child XML documents are not aggregated)"}
		}
		if strings.ContainsRune(o.SuppressionsProfile, 0) {
			return &OptionError{Field: "suppressions_profile", Reason: "must not contain NUL"}
		}
		return nil
	}
	if o.LeakCheck == "" {
		o.LeakCheck = LeakCheckFull
	}
	switch o.LeakCheck {
	case LeakCheckNone, LeakCheckSummary, LeakCheckFull:
	default:
		return &OptionError{Field: "leak_check", Reason: fmt.Sprintf("must be one of %q, %q, %q",
			LeakCheckNone, LeakCheckSummary, LeakCheckFull)}
	}

	if o.ShowLeakKinds == "" {
		o.ShowLeakKinds = ShowLeakKindsAll
	}
	switch o.ShowLeakKinds {
	case ShowLeakKindsAll, ShowLeakKindsDefinite, ShowLeakKindsPossible,
		ShowLeakKindsReachable, ShowLeakKindsDefinitePossible:
	default:
		return &OptionError{Field: "show_leak_kinds", Reason: "must be an allowlisted leak-kind selector"}
	}

	if o.NumCallers == 0 {
		o.NumCallers = DefaultNumCallers
	}
	if o.NumCallers < MinNumCallers || o.NumCallers > MaxNumCallers {
		return &OptionError{Field: "num_callers", Reason: fmt.Sprintf("must be between %d and %d",
			MinNumCallers, MaxNumCallers)}
	}

	if o.TraceChildren {
		return &OptionError{Field: "trace_children", Reason: "is not supported (separate child XML documents are not aggregated)"}
	}
	if strings.ContainsRune(o.SuppressionsProfile, 0) {
		return &OptionError{Field: "suppressions_profile", Reason: "must not contain NUL"}
	}
	return nil
}

// Map returns a deterministic, JSON-friendly description for the report.
func (o Options) Map() map[string]any {
	tool := o.Tool
	if tool == "" {
		tool = ToolMemcheck
	}
	values := map[string]any{
		"tool":                 string(tool),
		"num_callers":          o.NumCallers,
		"suppressions_profile": o.SuppressionsProfile,
		"trace_children":       o.TraceChildren,
	}
	if tool == ToolMemcheck {
		values["leak_check"] = string(o.LeakCheck)
		values["show_leak_kinds"] = string(o.ShowLeakKinds)
		values["track_origins"] = o.TrackOrigins
	}
	return values
}

// Arguments builds the complete Valgrind argument list. The program path and
// its arguments follow a "--" separator, so a target argument that looks like
// a Valgrind option cannot be reinterpreted by Valgrind.
func (o Options) Arguments(xmlPath, suppressionsPath, program string, programArgs []string) ([]string, error) {
	tool := o.Tool
	if tool == "" {
		tool = ToolMemcheck
	}
	return o.ArgumentsFor(tool, xmlPath, suppressionsPath, program, programArgs)
}

// ArgumentsFor builds the Valgrind argument list for tool. The program path and
// its arguments follow "--", so they cannot be reinterpreted as Valgrind flags.
func (o Options) ArgumentsFor(tool Tool, xmlPath, suppressionsPath, program string, programArgs []string) ([]string, error) {
	if err := o.ValidateFor(tool); err != nil {
		return nil, err
	}
	if !tool.Valid() {
		return nil, &OptionError{Field: "tool", Reason: "is not implemented"}
	}
	if program == "" || !filepath.IsAbs(program) {
		return nil, &OptionError{Field: "program", Reason: "must be an absolute path"}
	}
	if xmlPath == "" || !filepath.IsAbs(xmlPath) {
		return nil, &OptionError{Field: "xml_file", Reason: "must be an absolute path"}
	}
	if strings.ContainsRune(xmlPath, 0) || strings.ContainsRune(program, 0) {
		return nil, &OptionError{Field: "command", Reason: "must not contain NUL"}
	}

	args := []string{
		"--tool=" + string(tool),
		"--xml=yes",
		"--xml-file=" + xmlPath,
		fmt.Sprintf("--error-exitcode=%d", ErrorExitCode),
	}
	if tool == ToolMemcheck {
		args = append(args,
			"--leak-check="+string(o.LeakCheck),
			"--show-leak-kinds="+string(o.ShowLeakKinds))
		if o.TrackOrigins {
			args = append(args, "--track-origins=yes")
		}
	}
	args = append(args, fmt.Sprintf("--num-callers=%d", o.NumCallers))
	if o.SuppressionsProfile != "" {
		if suppressionsPath == "" || !filepath.IsAbs(suppressionsPath) {
			return nil, &OptionError{Field: "suppressions", Reason: "profile path must be absolute"}
		}
		if strings.ContainsRune(suppressionsPath, 0) {
			return nil, &OptionError{Field: "suppressions", Reason: "must not contain NUL"}
		}
		args = append(args, "--suppressions="+suppressionsPath)
	}

	args = append(args, "--", program)
	for _, arg := range programArgs {
		if strings.ContainsRune(arg, 0) {
			return nil, &OptionError{Field: "program_args", Reason: "argument contains NUL"}
		}
		args = append(args, arg)
	}
	return args, nil
}
