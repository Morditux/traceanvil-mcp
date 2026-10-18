package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
)

const (
	maxHeaderFieldBytes  = 1 << 20
	maxHeaderWarnings    = 32
	maxWarningBytes      = 4096
	maxHeaderArtifacts   = 64
	maxManifestBytes     = 1 << 20
	maxReportOccurrences = 1_000_000_000
	maxReportRecords     = 50_000
	diagnosticCheckEvery = 256
)

// reportHeader is the compact part of analysis.json reused by status and by
// every report page. Diagnostics are streamed separately.
type reportHeader struct {
	SchemaVersion      int
	RunID              string
	Tool               diagnostic.Tool
	Result             diagnostic.Result
	AnalysisComplete   bool
	Program            diagnostic.Program
	Summary            diagnostic.Summary
	Artifacts          []diagnostic.Artifact
	Warnings           []string
	WarningsTruncated  bool
	ArtifactsTruncated bool
}

type reportReadOptions struct {
	skipDiagnostics    bool
	allDiagnostics     bool
	filter             filter
	offset             int
	limit              int
	maxPageBytes       int64
	maxDiagnosticBytes int64
	maxReportBytes     int64
}

type reportStats struct {
	Matched           int
	TotalOccurrences  int64
	ReadableRecords   int
	Returned          int
	Consumed          int
	OmittedOversized  int
	Unreadable        int
	ByteBudgetReached bool
	ScanIncomplete    bool
}

type reportReadResult struct {
	Header      reportHeader
	Diagnostics []diagnostic.Diagnostic
	Stats       reportStats
}

// readReport streams a canonical report. It never loads the whole document and
// never derives "no diagnostics" from a parse failure: every structural or
// invariant violation returns report_invalid.
func readReport(ctx context.Context, reader io.Reader, size int64, opts reportReadOptions) (reportReadResult, error) {
	if opts.maxReportBytes <= 0 {
		return reportReadResult{}, Errorf(CodeInternal, "report read bound is not configured")
	}
	if size > opts.maxReportBytes {
		return reportReadResult{}, Errorf(CodeReportTooLarge,
			"analysis report is %d bytes; the API read bound is %d bytes", size, opts.maxReportBytes)
	}

	dec := json.NewDecoder(io.LimitReader(reader, opts.maxReportBytes))
	start, err := dec.Token()
	if err != nil {
		return reportReadResult{}, Wrap(CodeReportInvalid, err, "analysis report is not valid JSON")
	}
	if delim, ok := start.(json.Delim); !ok || delim != '{' {
		return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report root must be a JSON object")
	}

	result := reportReadResult{
		Header: reportHeader{
			Artifacts: []diagnostic.Artifact{},
			Warnings:  []string{},
		},
		Diagnostics: []diagnostic.Diagnostic{},
	}
	seen := make(map[string]bool)
	checkCounter := 0

	for dec.More() {
		checkCounter++
		if checkCounter%diagnosticCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return reportReadResult{}, contextError(err)
			}
		}

		keyToken, err := dec.Token()
		if err != nil {
			return reportReadResult{}, Wrap(CodeReportInvalid, err, "analysis report is truncated")
		}
		key, ok := keyToken.(string)
		if !ok {
			return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report contains a non-string key")
		}
		if seen[key] {
			return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report repeats the %q field", key)
		}
		seen[key] = true
		valueStart := dec.InputOffset()

		switch key {
		case "schema_version":
			if err := dec.Decode(&result.Header.SchemaVersion); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode schema_version")
			}
		case "run_id":
			if err := dec.Decode(&result.Header.RunID); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode run_id")
			}
		case "tool":
			if err := dec.Decode(&result.Header.Tool); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode tool")
			}
		case "result":
			if err := dec.Decode(&result.Header.Result); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode result")
			}
		case "analysis_complete":
			if err := dec.Decode(&result.Header.AnalysisComplete); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode analysis_complete")
			}
		case "program":
			if err := dec.Decode(&result.Header.Program); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode program")
			}
		case "summary":
			if err := dec.Decode(&result.Header.Summary); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode summary")
			}
		case "artifacts":
			if err := dec.Decode(&result.Header.Artifacts); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode artifacts")
			}
		case "warnings":
			if err := dec.Decode(&result.Header.Warnings); err != nil {
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "decode warnings")
			}
		case "diagnostics":
			if opts.skipDiagnostics {
				if err := skipValue(ctx, dec); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return reportReadResult{}, contextError(ctxErr)
					}
					return reportReadResult{}, Wrap(CodeReportInvalid, err, "skip diagnostics")
				}
				break
			}
			items, stats, err := readDiagnostics(ctx, dec, opts)
			if err != nil {
				return reportReadResult{}, err
			}
			result.Diagnostics = items
			result.Stats = stats
		default:
			if err := skipValue(ctx, dec); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return reportReadResult{}, contextError(ctxErr)
				}
				return reportReadResult{}, Wrap(CodeReportInvalid, err, "skip field %q", key)
			}
		}

		if key != "diagnostics" && dec.InputOffset()-valueStart > maxHeaderFieldBytes {
			return reportReadResult{}, Errorf(CodeReportInvalid,
				"analysis report field %q exceeds the %d-byte header bound", key, maxHeaderFieldBytes)
		}
	}

	end, err := dec.Token()
	if err != nil {
		return reportReadResult{}, Wrap(CodeReportInvalid, err, "analysis report is truncated")
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report is not a complete object")
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil {
		return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report has trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return reportReadResult{}, Wrap(CodeReportInvalid, err, "analysis report has trailing data")
	}

	if err := validateHeader(&result.Header, seen); err != nil {
		return reportReadResult{}, err
	}
	if !opts.skipDiagnostics && !seen["diagnostics"] {
		return reportReadResult{}, Errorf(CodeReportInvalid, "analysis report is missing %q", "diagnostics")
	}
	if !opts.skipDiagnostics && result.Stats.Unreadable == 0 && !result.Header.Summary.Truncated {
		if int64(result.Header.Summary.Total) != result.Stats.TotalOccurrences {
			return reportReadResult{}, Errorf(CodeReportInvalid,
				"summary total %d does not match %d parsed occurrences",
				result.Header.Summary.Total, result.Stats.TotalOccurrences)
		}
		if result.Header.Summary.DiagnosticRecords != result.Stats.ReadableRecords {
			return reportReadResult{}, Errorf(CodeReportInvalid,
				"summary diagnostic_records %d does not match %d parsed records",
				result.Header.Summary.DiagnosticRecords, result.Stats.ReadableRecords)
		}
	}
	result.Header.Warnings, result.Header.WarningsTruncated = normalizeWarnings(result.Header.Warnings)
	if len(result.Header.Artifacts) > maxHeaderArtifacts {
		result.Header.Artifacts = result.Header.Artifacts[:maxHeaderArtifacts]
		result.Header.ArtifactsTruncated = true
	}
	if result.Header.Artifacts == nil {
		result.Header.Artifacts = []diagnostic.Artifact{}
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []diagnostic.Diagnostic{}
	}
	result.Stats.Returned = len(result.Diagnostics)
	return result, nil
}

func readDiagnostics(ctx context.Context, dec *json.Decoder, opts reportReadOptions) ([]diagnostic.Diagnostic, reportStats, error) {
	stats := reportStats{}
	items := []diagnostic.Diagnostic{}
	accumulator := newPageAccumulator(opts.filter, opts.offset, opts.limit, opts.maxPageBytes)
	seenIDs := make(map[string]struct{})
	checked := 0

	if err := expectArray(dec); err != nil {
		return nil, stats, err
	}

	for dec.More() {
		checked++
		if checked > maxReportRecords {
			return nil, stats, Errorf(CodeReportTooLarge,
				"analysis report contains more than %d diagnostic records", maxReportRecords)
		}
		if checked%diagnosticCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, stats, contextError(err)
			}
		}

		valueStart := dec.InputOffset()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, stats, Wrap(CodeReportInvalid, err, "diagnostic record is truncated")
		}
		rawSize := dec.InputOffset() - valueStart
		if rawSize > opts.maxDiagnosticBytes {
			// The record is too large for a bounded response and cannot be
			// filtered safely. Matching counts are explicitly incomplete.
			stats.Unreadable++
			stats.ScanIncomplete = true
			continue
		}

		var item diagnostic.Diagnostic
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, stats, Wrap(CodeReportInvalid, err, "diagnostic record is not valid JSON")
		}
		if err := validateStoredDiagnostic(item); err != nil {
			return nil, stats, err
		}
		if _, duplicate := seenIDs[item.ID]; duplicate {
			return nil, stats, Errorf(CodeReportInvalid, "diagnostic id %q is duplicated", item.ID)
		}
		seenIDs[item.ID] = struct{}{}
		stats.ReadableRecords++
		stats.TotalOccurrences += int64(item.Occurrences)
		if opts.allDiagnostics {
			if !opts.filter.matches(item) {
				continue
			}
			stats.Matched++
			stats.Consumed++
			items = append(items, item)
			continue
		}
		if err := accumulator.offer(item); err != nil {
			return nil, stats, err
		}
	}

	if err := consumeArrayEnd(dec); err != nil {
		return nil, stats, err
	}
	if stats.Unreadable > 0 {
		stats.ScanIncomplete = true
	}
	if !opts.allDiagnostics {
		items, stats = accumulator.result(stats)
	}
	return items, stats, nil
}

// pageAccumulator turns a stream of validated diagnostics into one bounded
// page. It is shared by the streaming reader and the decoded-report cache so
// both enforce identical offset, limit, oversize and byte-budget semantics.
type pageAccumulator struct {
	filter       filter
	offset       int
	limit        int
	maxPageBytes int64

	matched       int
	consumed      int
	used          int64
	collected     int
	budgetStopped bool
	limitReached  bool
	byteBudget    bool
	omitted       int
	items         []diagnostic.Diagnostic
}

func newPageAccumulator(filter filter, offset, limit int, maxPageBytes int64) *pageAccumulator {
	return &pageAccumulator{
		filter:       filter,
		offset:       offset,
		limit:        limit,
		maxPageBytes: maxPageBytes,
		items:        []diagnostic.Diagnostic{},
	}
}

func (a *pageAccumulator) offer(item diagnostic.Diagnostic) error {
	if !a.filter.matches(item) {
		return nil
	}
	a.matched++
	if a.matched <= a.offset || a.limitReached || a.budgetStopped {
		return nil
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return Wrap(CodeReportInvalid, err, "re-encode diagnostic")
	}
	size := int64(len(encoded))
	if size > a.maxPageBytes {
		a.omitted++
		a.consumed++
		return nil
	}
	if a.collected > 0 && a.used+size > a.maxPageBytes {
		a.byteBudget = true
		a.budgetStopped = true
		return nil
	}
	a.items = append(a.items, item)
	a.used += size
	a.collected++
	a.consumed++
	if a.collected >= a.limit {
		a.limitReached = true
	}
	return nil
}

func (a *pageAccumulator) result(base reportStats) ([]diagnostic.Diagnostic, reportStats) {
	base.Matched = a.matched
	base.Consumed = a.consumed
	base.ByteBudgetReached = a.byteBudget
	base.OmittedOversized = a.omitted
	return a.items, base
}

func expectArray(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return Wrap(CodeReportInvalid, err, "diagnostics array is truncated")
	}
	if delim, ok := token.(json.Delim); !ok || delim != '[' {
		return Errorf(CodeReportInvalid, "diagnostics must be a JSON array")
	}
	return nil
}

func consumeArrayEnd(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return Wrap(CodeReportInvalid, err, "diagnostics array is truncated")
	}
	if delim, ok := token.(json.Delim); !ok || delim != ']' {
		return Errorf(CodeReportInvalid, "diagnostics array is not terminated")
	}
	return nil
}

// skipValue consumes one JSON value token by token. It is used instead of
// decoding unknown values so a skipped diagnostics array never has to be
// materialized as one allocation. It honors cancellation for large documents.
func skipValue(ctx context.Context, dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	depth := 1
	tokens := 0
	for depth > 0 {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		tokens++
		if tokens%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func validateHeader(header *reportHeader, seen map[string]bool) error {
	for _, required := range []string{"schema_version", "run_id", "tool", "result", "analysis_complete", "summary"} {
		if !seen[required] {
			return Errorf(CodeReportInvalid, "analysis report is missing %q", required)
		}
	}
	if header.SchemaVersion != diagnostic.SchemaVersion {
		return Errorf(CodeReportInvalid, "unsupported report schema_version %d", header.SchemaVersion)
	}
	if header.RunID == "" {
		return Errorf(CodeReportInvalid, "analysis report run_id is empty")
	}
	if header.Tool.Name == "" {
		return Errorf(CodeReportInvalid, "analysis report tool name is empty")
	}
	if !header.Result.Valid() {
		return Errorf(CodeReportInvalid, "analysis report result %q is invalid", header.Result)
	}
	if header.Result == diagnostic.ResultNoErrorsObserved && !header.AnalysisComplete {
		return Errorf(CodeReportInvalid, "no_errors_observed with analysis_complete=false")
	}
	if header.Result == diagnostic.ResultErrorsFound && header.Summary.Total == 0 {
		return Errorf(CodeReportInvalid, "errors_found with a zero total")
	}
	if header.Summary.ByKind == nil {
		header.Summary.ByKind = map[string]int{}
	}
	if header.Summary.BySeverity == nil {
		header.Summary.BySeverity = map[string]int{}
	}
	if header.Warnings == nil {
		header.Warnings = []string{}
	}
	if header.Artifacts == nil {
		header.Artifacts = []diagnostic.Artifact{}
	}
	return nil
}

func validateStoredDiagnostic(item diagnostic.Diagnostic) error {
	if item.ID == "" || len(item.ID) > 128 {
		return Errorf(CodeReportInvalid, "diagnostic id is missing or exceeds 128 bytes")
	}
	if item.Kind == "" || len(item.Kind) > 128 {
		return Errorf(CodeReportInvalid, "diagnostic %q has a missing or oversized kind", item.ID)
	}
	if !item.Severity.Valid() {
		return Errorf(CodeReportInvalid, "diagnostic %q has an invalid severity %q", item.ID, item.Severity)
	}
	if item.Confidence != diagnostic.ConfidenceToolReported && item.Confidence != diagnostic.ConfidenceInference {
		return Errorf(CodeReportInvalid, "diagnostic %q has an invalid confidence %q", item.ID, item.Confidence)
	}
	if !strings.HasPrefix(item.Fingerprint, "sha256:") {
		return Errorf(CodeReportInvalid, "diagnostic %q has no sha256 fingerprint", item.ID)
	}
	if item.Occurrences < 1 || item.Occurrences > maxReportOccurrences {
		return Errorf(CodeReportInvalid,
			"diagnostic %q has an out-of-range occurrence count %d", item.ID, item.Occurrences)
	}
	if item.Evidence.Artifact == "" {
		return Errorf(CodeReportInvalid, "diagnostic %q has no evidence artifact", item.ID)
	}
	return nil
}

// normalizeWarnings bounds warning text before it reaches an MCP response.
func normalizeWarnings(warnings []string) ([]string, bool) {
	if len(warnings) == 0 {
		return []string{}, false
	}
	truncated := false
	max := len(warnings)
	if max > maxHeaderWarnings {
		max = maxHeaderWarnings
		truncated = true
	}
	normalized := make([]string, 0, max+1)
	for _, warning := range warnings[:max] {
		warning = strings.TrimSpace(warning)
		if len(warning) > maxWarningBytes {
			warning = limitUTF8(warning, maxWarningBytes) + "..."
			truncated = true
		}
		normalized = append(normalized, warning)
	}
	if len(warnings) > maxHeaderWarnings {
		normalized = append(normalized, "additional report warnings were omitted")
	}
	return normalized, truncated
}

func limitUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func readJobManifest(store *artifacts.Store, runID string) (jobs.Snapshot, error) {
	file, err := store.OpenArtifact(runID, jobs.ManifestName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return jobs.Snapshot{}, err
		}
		return jobs.Snapshot{}, Wrap(CodeArtifactUnavailable, err, "job manifest is unavailable")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return jobs.Snapshot{}, Wrap(CodeArtifactUnavailable, err, "job manifest is unreadable")
	}
	if len(data) > maxManifestBytes {
		return jobs.Snapshot{}, Errorf(CodeArtifactUnavailable,
			"job manifest exceeds %d bytes", maxManifestBytes)
	}
	var snapshot jobs.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return jobs.Snapshot{}, Wrap(CodeArtifactUnavailable, err, "job manifest is not valid JSON")
	}
	if snapshot.SchemaVersion != jobs.SchemaVersion {
		return jobs.Snapshot{}, Errorf(CodeArtifactUnavailable,
			"unsupported job manifest schema_version %d", snapshot.SchemaVersion)
	}
	if snapshot.RunID != runID {
		return jobs.Snapshot{}, Errorf(CodeArtifactUnavailable,
			"job manifest run_id does not match the requested run")
	}
	return snapshot, nil
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "request deadline exceeded")
	}
	return Wrap(CodeCanceled, err, "request canceled")
}
