package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

const (
	maxDiffWarnings     = 20
	maxDiffWarningBytes = 512
)

// Diff compares two persisted analysis reports. It never launches a program,
// never reads a client-selected path and never turns incompatible metadata into
// an empty diff.
func (s *Service) Diff(ctx context.Context, req DiffRequest) (DiffResult, error) {
	if ctx == nil {
		return DiffResult{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return DiffResult{}, contextError(err)
	}
	if err := validateRunID(req.BaseRunID); err != nil {
		return DiffResult{}, err
	}
	if err := validateRunID(req.TargetRunID); err != nil {
		return DiffResult{}, err
	}
	if req.BaseRunID == req.TargetRunID {
		return DiffResult{}, Errorf(CodeInvalidArgument, "base_run_id and target_run_id must differ")
	}
	select {
	case s.diffSlots <- struct{}{}:
		defer func() { <-s.diffSlots }()
	case <-ctx.Done():
		return DiffResult{}, contextError(ctx.Err())
	}
	limit, err := s.pageLimit(req.Limit)
	if err != nil {
		return DiffResult{}, err
	}
	hash := diffCursorHash(req.BaseRunID, req.TargetRunID)
	offset, err := decodeCursor(req.Cursor, req.BaseRunID, hash)
	if err != nil {
		return DiffResult{}, err
	}

	base, err := s.loadFullReport(ctx, req.BaseRunID)
	if err != nil {
		return DiffResult{}, err
	}
	target, err := s.loadFullReport(ctx, req.TargetRunID)
	if err != nil {
		return DiffResult{}, err
	}
	compared, err := diagnostic.Compare(base, target)
	if err != nil {
		var incompatible *diagnostic.IncompatibleError
		if errors.As(err, &incompatible) {
			return DiffResult{}, Wrap(CodeIncompatibleRuns, err, "%s", incompatible.Error())
		}
		return DiffResult{}, Wrap(CodeReportInvalid, err, "compare analysis reports")
	}

	total := len(compared.Items)
	if offset > total {
		return DiffResult{}, Errorf(CodeInvalidCursor, "diff cursor offset %d exceeds %d items", offset, total)
	}
	warnings := make([]string, 0, len(base.Warnings)+len(target.Warnings)+2)
	for _, warning := range base.Warnings {
		warnings = append(warnings, "base: "+warning)
	}
	for _, warning := range target.Warnings {
		warnings = append(warnings, "target: "+warning)
	}
	if len(compared.Compatibility.Changes) > 0 {
		warnings = append(warnings,
			"run metadata changed; see compatibility.changes for the observed differences")
	}
	if len(compared.Compatibility.Uncertainties) > 0 {
		warnings = append(warnings,
			"run metadata or configuration is not fully comparable; items are classified uncertain")
	}
	warnings = boundDiffWarnings(warnings)

	result := DiffResult{
		SchemaVersion: DiffSchemaVersion,
		BaseRunID:     req.BaseRunID,
		TargetRunID:   req.TargetRunID,
		Compatibility: compared.Compatibility,
		Summary:       compared.Summary,
		Page: PageInfo{
			Offset:  offset,
			Limit:   limit,
			Matched: total,
		},
		Items:    []diagnostic.DiffItem{},
		Warnings: warnings,
	}
	// Budget the whole response, not just the item array. The 512-byte reserve
	// covers page counters, a cursor and the final byte-budget warning.
	envelope, err := json.Marshal(result)
	if err != nil {
		return DiffResult{}, Wrap(CodeInternal, err, "encode diff envelope")
	}
	available := s.maxPageBytes - int64(len(envelope)) - 512
	if available <= 0 {
		return DiffResult{}, Errorf(CodeDiffTooLarge,
			"the diff response envelope exceeds the %d-byte response bound", s.maxPageBytes)
	}
	items := make([]diagnostic.DiffItem, 0, limit)
	usedBytes := int64(0)
	byteBudgetReached := false
	for index := offset; index < total && len(items) < limit; index++ {
		encoded, err := json.Marshal(compared.Items[index])
		if err != nil {
			return DiffResult{}, Wrap(CodeInternal, err, "encode diff item")
		}
		if int64(len(encoded)) > available {
			if len(items) == 0 {
				return DiffResult{}, Errorf(CodeDiffTooLarge,
					"one diff item is %d bytes; the response byte bound is %d", len(encoded), s.maxPageBytes)
			}
			byteBudgetReached = true
			break
		}
		if usedBytes+int64(len(encoded)) > available && len(items) > 0 {
			byteBudgetReached = true
			break
		}
		items = append(items, compared.Items[index])
		usedBytes += int64(len(encoded))
	}
	result.Items = items
	result.Page.Returned = len(items)
	result.Page.Consumed = offset + len(items)
	result.Page.ByteBudgetReached = byteBudgetReached
	updateDiffPage(&result, req.BaseRunID, hash)
	if byteBudgetReached {
		result.Warnings = boundDiffWarnings(append(result.Warnings,
			"the diff page stopped at the response byte bound; continue with page.next_cursor"))
	}
	if err := fitDiffResponse(&result, req.BaseRunID, hash, s.maxPageBytes); err != nil {
		return DiffResult{}, err
	}

	return result, nil
}

func (s *Service) loadFullReport(ctx context.Context, runID string) (diagnostic.Report, error) {
	file, err := s.store.OpenArtifact(runID, valgrind.AnalysisArtifactName)
	if err != nil {
		return diagnostic.Report{}, s.artifactError(runID, valgrind.AnalysisArtifactName, err,
			CodeReportUnavailable, "analysis report is unavailable for run %q", runID)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return diagnostic.Report{}, Wrap(CodeReportUnavailable, err, "inspect analysis report")
	}
	read, err := readReport(ctx, file, info.Size(), reportReadOptions{
		allDiagnostics:     true,
		maxPageBytes:       s.maxPageBytes,
		maxDiagnosticBytes: s.maxDiagnosticSize,
		maxReportBytes:     s.maxReportBytes,
	})
	if err != nil {
		return diagnostic.Report{}, err
	}
	if read.Header.RunID != runID {
		return diagnostic.Report{}, Errorf(CodeReportInvalid,
			"analysis report run_id %q does not match the requested run", read.Header.RunID)
	}
	report := diagnostic.Report{
		SchemaVersion:    read.Header.SchemaVersion,
		RunID:            read.Header.RunID,
		Tool:             read.Header.Tool,
		Result:           read.Header.Result,
		AnalysisComplete: read.Header.AnalysisComplete,
		Program:          read.Header.Program,
		Metadata:         read.Header.Metadata,
		Options:          read.Header.Options,
		Summary:          read.Header.Summary,
		Diagnostics:      read.Diagnostics,
		Suppressions:     read.Header.Suppressions,
		Artifacts:        read.Header.Artifacts,
		Warnings:         read.Header.Warnings,
	}
	if read.Header.WarningsTruncated {
		report.Warnings = append(report.Warnings, "report warnings were truncated")
	}
	if read.Stats.Unreadable > 0 || read.Stats.ScanIncomplete {
		report.Summary.Truncated = true
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("report contains %d unreadable diagnostic record(s); comparison evidence is incomplete",
				read.Stats.Unreadable))
	}
	if err := report.Validate(); err != nil {
		return diagnostic.Report{}, Wrap(CodeReportInvalid, err, "validate analysis report")
	}
	return report, nil
}

func updateDiffPage(result *DiffResult, baseRunID, hash string) {
	result.Page.HasMore = result.Page.Consumed < result.Page.Matched
	result.Page.NextCursor = ""
	if !result.Page.HasMore {
		return
	}
	cursor, err := encodeCursor(baseRunID, result.Page.Consumed, hash)
	if err == nil {
		result.Page.NextCursor = cursor
	}
}

func fitDiffResponse(result *DiffResult, baseRunID, hash string, maxBytes int64) error {
	for {
		encoded, err := json.Marshal(result)
		if err != nil {
			return Wrap(CodeInternal, err, "encode diff response")
		}
		if int64(len(encoded)) <= maxBytes {
			return nil
		}
		if len(result.Items) == 0 {
			return Errorf(CodeDiffTooLarge,
				"the diff response envelope exceeds the %d-byte response bound", maxBytes)
		}
		result.Items = result.Items[:len(result.Items)-1]
		result.Page.Returned = len(result.Items)
		result.Page.Consumed = result.Page.Offset + len(result.Items)
		result.Page.ByteBudgetReached = true
		updateDiffPage(result, baseRunID, hash)
		result.Warnings = boundDiffWarnings(append(result.Warnings,
			"the diff page stopped at the response byte bound; continue with page.next_cursor"))
	}
}

func diffCursorHash(baseRunID, targetRunID string) string {
	sum := sha256.Sum256([]byte("diff-v1\x00" + baseRunID + "\x00" + targetRunID))
	return "diff:" + hex.EncodeToString(sum[:])
}

func boundDiffWarnings(warnings []string) []string {
	seen := make(map[string]struct{}, len(warnings))
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if len(warning) > maxDiffWarningBytes {
			warning = limitUTF8(warning, maxDiffWarningBytes) + "..."
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		result = append(result, warning)
		if len(result) >= maxDiffWarnings {
			result = append(result, fmt.Sprintf("warnings truncated to %d entries", maxDiffWarnings))
			break
		}
	}
	return result
}
