package diagnostic

import (
	"path/filepath"
	"strings"
)

// NormalizePath converts a tool-reported source path into a stable,
// slash-separated path. Paths inside a workspace root are made relative to that
// root; other paths stay absolute. It never resolves symlinks: normalization
// must not claim a filesystem identity it did not verify.
func NormalizePath(path string, roots []string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}

	clean := filepath.Clean(path)
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), clean)
		if err != nil {
			continue
		}
		if rel == "." {
			return "."
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(clean)
}

// SelectLocation returns the first non-runtime frame that names a source file.
// It falls back to the first non-runtime frame with a function, then to the
// first frame at all. Missing file or line information stays missing: the
// server does not invent a source location.
func SelectLocation(stack []Frame) *Location {
	for _, preferPath := range []bool{true, false} {
		for _, frame := range stack {
			if frame.Runtime {
				continue
			}
			if preferPath && frame.Path == "" {
				continue
			}
			if !preferPath && frame.Function == "" && frame.Path == "" {
				continue
			}
			return &Location{Function: frame.Function, Path: frame.Path, Line: frame.Line}
		}
	}
	return nil
}

// BuildSummary aggregates diagnostics. Total sums tool-reported occurrences;
// DiagnosticRecords counts distinct records.
func BuildSummary(diagnostics []Diagnostic, suppressions []Suppression, truncated bool) Summary {
	summary := Summary{
		ByKind:     make(map[string]int),
		BySeverity: make(map[string]int),
		Truncated:  truncated,
	}
	summary.DiagnosticRecords = len(diagnostics)
	for _, d := range diagnostics {
		summary.Total += d.Occurrences
		summary.ByKind[d.Kind] += d.Occurrences
		summary.BySeverity[string(d.Severity)] += d.Occurrences
		summary.LeakedBytes += d.LeakedBytes
		summary.LeakedBlocks += d.LeakedBlocks
	}
	for _, suppression := range suppressions {
		summary.Suppressed += suppression.Count
	}
	return summary
}
