package diagnostic

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Change classifies one fingerprint group in a run comparison.
type Change string

const (
	// ChangeNew is present in the target run and absent from the base run.
	ChangeNew Change = "new"
	// ChangeResolved is present in the base run and absent from the target run.
	ChangeResolved Change = "resolved"
	// ChangePersistent is present in both runs with the same observable fields.
	ChangePersistent Change = "persistent"
	// ChangeChanged is present in both runs but its counts or severity changed.
	ChangeChanged Change = "changed"
	// ChangeUncertain cannot be classified safely because fingerprints are
	// duplicated, a report is incomplete, or run metadata/config differs.
	ChangeUncertain Change = "uncertain"
)

// Valid reports whether c is a known change classification.
func (c Change) Valid() bool {
	switch c {
	case ChangeNew, ChangeResolved, ChangePersistent, ChangeChanged, ChangeUncertain:
		return true
	default:
		return false
	}
}

// Compatibility describes whether two reports were compared with the same
// fingerprint recipe and whether observed configuration differences prevent a
// definite classification.
type Compatibility struct {
	FingerprintAlgorithm string   `json:"fingerprint_algorithm"`
	Changes              []string `json:"changes,omitempty"`
	Uncertainties        []string `json:"uncertainties,omitempty"`
}

// DiffSummary aggregates items and tool-reported occurrences. Counts classify
// fingerprint groups, not raw diagnostic records.
type DiffSummary struct {
	BaseDiagnostics   int `json:"base_diagnostics"`
	TargetDiagnostics int `json:"target_diagnostics"`
	BaseOccurrences   int `json:"base_occurrences"`
	TargetOccurrences int `json:"target_occurrences"`

	New        int `json:"new"`
	Resolved   int `json:"resolved"`
	Persistent int `json:"persistent"`
	Changed    int `json:"changed"`
	Uncertain  int `json:"uncertain"`
}

// DiagnosticRef is the bounded evidence shown for one side of a diff item.
// Full stacks remain available through analysis_report; this view is compact
// enough for a paginated comparison.
type DiagnosticRef struct {
	ID                   string     `json:"id"`
	Fingerprint          string     `json:"fingerprint"`
	FingerprintAlgorithm string     `json:"fingerprint_algorithm"`
	Origin               string     `json:"origin"`
	Kind                 string     `json:"kind"`
	Severity             Severity   `json:"severity"`
	Message              string     `json:"message"`
	Location             *Location  `json:"location,omitempty"`
	Occurrences          int        `json:"occurrences"`
	LeakedBytes          int64      `json:"leaked_bytes,omitempty"`
	LeakedBlocks         int64      `json:"leaked_blocks,omitempty"`
	Evidence             Evidence   `json:"evidence"`
	Confidence           Confidence `json:"confidence"`
}

// DiffItem is one fingerprint group, or one ambiguous group when duplicate
// fingerprints make pairing unsafe. BaseCount/TargetCount are greater than one
// only for an uncertain duplicate group; only the first record of each side is
// summarized.
type DiffItem struct {
	Change      Change         `json:"change"`
	Base        *DiagnosticRef `json:"base,omitempty"`
	Target      *DiagnosticRef `json:"target,omitempty"`
	BaseCount   int            `json:"base_count,omitempty"`
	TargetCount int            `json:"target_count,omitempty"`
	Reasons     []string       `json:"reasons,omitempty"`
}

// DiffResult is the deterministic core comparison. Pagination and response
// byte bounds are owned by the analysis API.
type DiffResult struct {
	BaseRunID     string        `json:"base_run_id"`
	TargetRunID   string        `json:"target_run_id"`
	Compatibility Compatibility `json:"compatibility"`
	Summary       DiffSummary   `json:"summary"`
	Items         []DiffItem    `json:"items"`
}

// IncompatibleError means the reports cannot be compared honestly. It is
// distinct from an incomplete comparison, which is represented as uncertain
// items in a normal DiffResult.
type IncompatibleError struct {
	Reasons []string
}

func (e *IncompatibleError) Error() string {
	if e == nil || len(e.Reasons) == 0 {
		return "reports are not comparable"
	}
	return "reports are not comparable: " + strings.Join(e.Reasons, "; ")
}

// Compare pairs diagnostics by fingerprint and classifies new, resolved,
// persistent and changed groups. It deliberately returns an error when the
// reports are structurally incompatible; it never degrades that case into an
// empty or falsely resolved diff.
func Compare(base, target Report) (DiffResult, error) {
	if base.SchemaVersion != SchemaVersion || target.SchemaVersion != SchemaVersion ||
		base.SchemaVersion != target.SchemaVersion {
		return DiffResult{}, &IncompatibleError{Reasons: []string{
			fmt.Sprintf("report schema versions are not comparable (%d vs %d)",
				base.SchemaVersion, target.SchemaVersion),
		}}
	}
	if err := base.Validate(); err != nil {
		return DiffResult{}, fmt.Errorf("base report: %w", err)
	}
	if err := target.Validate(); err != nil {
		return DiffResult{}, fmt.Errorf("target report: %w", err)
	}

	algorithm, hard, changes, uncertainties := compareMetadata(base, target)
	if len(hard) > 0 {
		return DiffResult{}, &IncompatibleError{Reasons: boundReasons(hard)}
	}

	result := DiffResult{
		BaseRunID:   base.RunID,
		TargetRunID: target.RunID,
		Compatibility: Compatibility{
			FingerprintAlgorithm: algorithm,
			Changes:              dedupeStrings(changes),
			Uncertainties:        dedupeStrings(uncertainties),
		},
	}
	result.Summary = summarize(base, target)

	baseGroups := groupDiagnostics(base.Diagnostics)
	targetGroups := groupDiagnostics(target.Diagnostics)
	keys := make([]string, 0, len(baseGroups)+len(targetGroups))
	seen := make(map[string]struct{}, len(baseGroups)+len(targetGroups))
	for key := range baseGroups {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range targetGroups {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	globalUncertain := len(result.Compatibility.Uncertainties) > 0
	for _, key := range keys {
		baseGroup := baseGroups[key]
		targetGroup := targetGroups[key]
		item := compareGroup(key, baseGroup, targetGroup)
		if globalUncertain && item.Change != ChangeUncertain {
			item.Change = ChangeUncertain
			item.Reasons = append(item.Reasons, "run metadata or configuration differs; the classification is not definitive")
		}
		if len(item.Reasons) > 0 {
			item.Reasons = dedupeStrings(item.Reasons)
		}
		result.Items = append(result.Items, item)
		switch item.Change {
		case ChangeNew:
			result.Summary.New++
		case ChangeResolved:
			result.Summary.Resolved++
		case ChangePersistent:
			result.Summary.Persistent++
		case ChangeChanged:
			result.Summary.Changed++
		case ChangeUncertain:
			result.Summary.Uncertain++
		}
	}
	if result.Items == nil {
		result.Items = []DiffItem{}
	}
	return result, nil
}

func compareGroup(fingerprint string, base, target []Diagnostic) DiffItem {
	item := DiffItem{}
	if len(base) > 0 {
		item.Base = newDiagnosticRef(base[0])
	}
	if len(target) > 0 {
		item.Target = newDiagnosticRef(target[0])
	}
	if len(base) > 1 {
		item.Reasons = append(item.Reasons, fmt.Sprintf("fingerprint %s appears %d times in the base run", fingerprint, len(base)))
	}
	if len(target) > 1 {
		item.Reasons = append(item.Reasons, fmt.Sprintf("fingerprint %s appears %d times in the target run", fingerprint, len(target)))
	}
	if len(base) > 1 || len(target) > 1 {
		item.BaseCount = len(base)
		item.TargetCount = len(target)
		item.Change = ChangeUncertain
		item.Reasons = append(item.Reasons, "duplicate fingerprints make pairing ambiguous")
		return item
	}

	switch {
	case len(base) == 0 && len(target) == 1:
		item.Change = ChangeNew
	case len(base) == 1 && len(target) == 0:
		item.Change = ChangeResolved
	case len(base) == 1 && len(target) == 1:
		if diagnosticObservablyChanged(base[0], target[0]) {
			item.Change = ChangeChanged
		} else {
			item.Change = ChangePersistent
		}
	default:
		item.Change = ChangeUncertain
		item.Reasons = append(item.Reasons, "fingerprint group is empty on both sides")
	}
	return item
}

func newDiagnosticRef(d Diagnostic) *DiagnosticRef {
	return &DiagnosticRef{
		ID:                   d.ID,
		Fingerprint:          d.Fingerprint,
		FingerprintAlgorithm: d.FingerprintAlgorithm,
		Origin:               d.Origin,
		Kind:                 d.Kind,
		Severity:             d.Severity,
		Message:              d.Message,
		Location:             d.Location,
		Occurrences:          d.Occurrences,
		LeakedBytes:          d.LeakedBytes,
		LeakedBlocks:         d.LeakedBlocks,
		Evidence:             d.Evidence,
		Confidence:           d.Confidence,
	}
}

func diagnosticObservablyChanged(base, target Diagnostic) bool {
	return base.Severity != target.Severity ||
		base.Occurrences != target.Occurrences ||
		base.LeakedBytes != target.LeakedBytes ||
		base.LeakedBlocks != target.LeakedBlocks
}

func groupDiagnostics(diagnostics []Diagnostic) map[string][]Diagnostic {
	groups := make(map[string][]Diagnostic, len(diagnostics))
	for _, item := range diagnostics {
		groups[item.Fingerprint] = append(groups[item.Fingerprint], item)
	}
	return groups
}

func summarize(base, target Report) DiffSummary {
	summary := DiffSummary{
		BaseDiagnostics:   len(base.Diagnostics),
		TargetDiagnostics: len(target.Diagnostics),
	}
	for _, item := range base.Diagnostics {
		summary.BaseOccurrences += item.Occurrences
	}
	for _, item := range target.Diagnostics {
		summary.TargetOccurrences += item.Occurrences
	}
	return summary
}

func compareMetadata(base, target Report) (algorithm string, hard, changes, uncertainties []string) {
	if base.SchemaVersion != SchemaVersion || target.SchemaVersion != SchemaVersion {
		hard = append(hard, "schema versions differ")
	}
	if base.Tool.Name != target.Tool.Name {
		hard = append(hard, fmt.Sprintf("tool names differ (%q vs %q)", base.Tool.Name, target.Tool.Name))
	}
	if base.Program.TargetID != "" && target.Program.TargetID != "" && base.Program.TargetID != target.Program.TargetID {
		hard = append(hard, fmt.Sprintf("target ids differ (%q vs %q)", base.Program.TargetID, target.Program.TargetID))
	} else if base.Program.TargetID == "" || target.Program.TargetID == "" {
		uncertainties = append(uncertainties, "one report has no target id")
	}
	if base.Metadata.OS != "" && target.Metadata.OS != "" && base.Metadata.OS != target.Metadata.OS {
		hard = append(hard, fmt.Sprintf("operating systems differ (%q vs %q)", base.Metadata.OS, target.Metadata.OS))
	}
	if base.Metadata.Arch != "" && target.Metadata.Arch != "" && base.Metadata.Arch != target.Metadata.Arch {
		hard = append(hard, fmt.Sprintf("architectures differ (%q vs %q)", base.Metadata.Arch, target.Metadata.Arch))
	}
	if base.Metadata.OS == "" || target.Metadata.OS == "" {
		uncertainties = append(uncertainties, "one report has no operating-system metadata")
	}
	if base.Metadata.Arch == "" || target.Metadata.Arch == "" {
		uncertainties = append(uncertainties, "one report has no architecture metadata")
	}

	algorithm = base.Metadata.FingerprintAlgorithm
	if algorithm == "" {
		algorithm = target.Metadata.FingerprintAlgorithm
	}
	if algorithm == "" {
		algorithm = firstDiagnosticAlgorithm(base.Diagnostics)
		if algorithm == "" {
			algorithm = firstDiagnosticAlgorithm(target.Diagnostics)
		}
	}
	if base.Metadata.FingerprintAlgorithm != "" && target.Metadata.FingerprintAlgorithm != "" &&
		base.Metadata.FingerprintAlgorithm != target.Metadata.FingerprintAlgorithm {
		hard = append(hard, fmt.Sprintf("fingerprint algorithms differ (%q vs %q)",
			base.Metadata.FingerprintAlgorithm, target.Metadata.FingerprintAlgorithm))
	}
	if algorithm == "" {
		hard = append(hard, "fingerprint algorithm metadata is missing and no diagnostic provides it")
	} else if !ValidFingerprintAlgorithm(algorithm) {
		hard = append(hard, fmt.Sprintf("fingerprint algorithm %q is not supported", algorithm))
	}
	if base.Metadata.FingerprintAlgorithm == "" || target.Metadata.FingerprintAlgorithm == "" {
		uncertainties = append(uncertainties, "one report has no fingerprint-algorithm metadata")
	}
	baseMismatch := 0
	for _, diagnostic := range base.Diagnostics {
		if diagnostic.FingerprintAlgorithm != algorithm {
			baseMismatch++
		}
	}
	targetMismatch := 0
	for _, diagnostic := range target.Diagnostics {
		if diagnostic.FingerprintAlgorithm != algorithm {
			targetMismatch++
		}
	}
	if baseMismatch > 0 {
		hard = append(hard, fmt.Sprintf("%d base diagnostic(s) use a different fingerprint algorithm than %q",
			baseMismatch, algorithm))
	}
	if targetMismatch > 0 {
		hard = append(hard, fmt.Sprintf("%d target diagnostic(s) use a different fingerprint algorithm than %q",
			targetMismatch, algorithm))
	}

	if base.Result == ResultToolError || base.Result == ResultPolicyDenied {
		hard = append(hard, "base run did not produce usable analysis evidence")
	}
	if target.Result == ResultToolError || target.Result == ResultPolicyDenied {
		hard = append(hard, "target run did not produce usable analysis evidence")
	}
	if base.Result == ResultIncomplete || target.Result == ResultIncomplete {
		uncertainties = append(uncertainties, "one or both runs are incomplete")
	}
	if !base.AnalysisComplete || !target.AnalysisComplete {
		uncertainties = append(uncertainties, "one or both reports are not complete")
	}
	if base.Summary.Truncated || target.Summary.Truncated {
		uncertainties = append(uncertainties, "one or both reports have truncated evidence")
	}

	if base.Tool.Version == "" || target.Tool.Version == "" {
		uncertainties = append(uncertainties, "one report has no tool version")
	} else if base.Tool.Version != target.Tool.Version {
		message := fmt.Sprintf("tool versions differ (%q vs %q)", shortDiffValue(base.Tool.Version), shortDiffValue(target.Tool.Version))
		changes = append(changes, message)
		uncertainties = append(uncertainties, message)
	}
	if base.Tool.Path == "" || target.Tool.Path == "" {
		uncertainties = append(uncertainties, "one report has no tool path")
	} else if base.Tool.Path != target.Tool.Path {
		changes = append(changes, "tool paths differ")
	}
	if base.Metadata.ParserVersion != target.Metadata.ParserVersion {
		message := fmt.Sprintf("parser versions differ (%q vs %q)",
			shortDiffValue(base.Metadata.ParserVersion), shortDiffValue(target.Metadata.ParserVersion))
		changes = append(changes, message)
		uncertainties = append(uncertainties, message)
	}
	if base.Metadata.ParserVersion == "" || target.Metadata.ParserVersion == "" {
		uncertainties = append(uncertainties, "one report has no parser version")
	}
	if !sameStringSlice(base.Metadata.WorkspaceRoots, target.Metadata.WorkspaceRoots) {
		changes = append(changes, "workspace roots differ")
		uncertainties = append(uncertainties, "workspace roots differ")
	}
	if !sameOptions(base.Options, target.Options) {
		changes = append(changes, "analysis options differ")
		uncertainties = append(uncertainties, "analysis options differ")
	}
	if base.Program.Path == "" || target.Program.Path == "" {
		uncertainties = append(uncertainties, "one report has no program path")
	} else if base.Program.Path != target.Program.Path {
		changes = append(changes, "program paths differ")
	}
	if base.Program.SHA256 == "" || target.Program.SHA256 == "" {
		uncertainties = append(uncertainties, "one report has no program hash")
	} else if base.Program.SHA256 != target.Program.SHA256 {
		changes = append(changes, "program binaries differ")
	}
	if !sameStringSlice(base.Program.Args, target.Program.Args) {
		changes = append(changes, "program argument lists differ")
		uncertainties = append(uncertainties, "program argument lists differ")
	}
	if base.Program.ExitCode != target.Program.ExitCode ||
		base.Program.Signal != target.Program.Signal ||
		base.Program.TimedOut != target.Program.TimedOut ||
		base.Program.Canceled != target.Program.Canceled {
		changes = append(changes, "program outcomes differ")
	}
	if !sameSuppressions(base.Suppressions, target.Suppressions) {
		changes = append(changes, "suppression counts differ")
		uncertainties = append(uncertainties, "suppression counts differ")
	}
	return algorithm, hard, changes, uncertainties
}

func shortDiffValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return limitUTF8String(value, 128) + "..."
	}
	return value
}

func boundReasons(reasons []string) []string {
	const maxReasons = 16
	const maxReasonBytes = 512
	seen := make(map[string]struct{}, len(reasons))
	result := make([]string, 0, maxReasons)
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		if len(reason) > maxReasonBytes {
			reason = limitUTF8String(reason, maxReasonBytes) + "..."
		}
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		result = append(result, reason)
		if len(result) >= maxReasons {
			break
		}
	}
	return result
}

func limitUTF8String(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit]
}

func firstDiagnosticAlgorithm(diagnostics []Diagnostic) string {
	for _, item := range diagnostics {
		return item.FingerprintAlgorithm
	}
	return ""
}

func sameStringSlice(base, target []string) bool {
	if len(base) != len(target) {
		return false
	}
	for i := range base {
		if base[i] != target[i] {
			return false
		}
	}
	return true
}

func sameOptions(base, target map[string]any) bool {
	if len(base) == 0 && len(target) == 0 {
		return true
	}
	return reflect.DeepEqual(base, target)
}

func sameSuppressions(base, target []Suppression) bool {
	baseMap := make(map[string]int, len(base))
	for _, item := range base {
		baseMap[item.Name+"\x00"+item.Unique] = item.Count
	}
	targetMap := make(map[string]int, len(target))
	for _, item := range target {
		targetMap[item.Name+"\x00"+item.Unique] = item.Count
	}
	if len(baseMap) != len(targetMap) {
		return false
	}
	for key, count := range baseMap {
		if targetCount, ok := targetMap[key]; !ok || targetCount != count {
			return false
		}
	}
	return true
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
