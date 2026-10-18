package analysis

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

const (
	maxCursorBytes      = 1024
	maxFilterKinds      = 32
	maxFilterStringSize = 4096
	maxFunctionBytes    = 1024
	maxFilterOccurrence = 1_000_000_000
)

var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// filter is the normalized, validated diagnostic filter. Empty slices and empty
// strings are inactive; all active conditions must hold.
type filter struct {
	kinds          []string
	severities     []diagnostic.Severity
	pathPrefix     string
	function       string
	fingerprint    string
	minOccurrences int
}

func parseFilter(req ReportRequest) (filter, error) {
	parsed := filter{
		pathPrefix:     req.PathPrefix,
		function:       req.Function,
		fingerprint:    req.Fingerprint,
		minOccurrences: req.MinOccurrences,
	}

	if len(req.Kinds) > maxFilterKinds {
		return filter{}, Errorf(CodeInvalidArgument, "kinds must contain at most %d values", maxFilterKinds)
	}
	seenKinds := make(map[string]struct{}, len(req.Kinds))
	for _, kind := range req.Kinds {
		if kind == "" {
			return filter{}, Errorf(CodeInvalidArgument, "kinds must not contain empty values")
		}
		if len(kind) > 128 {
			return filter{}, Errorf(CodeInvalidArgument, "kind %q exceeds 128 bytes", kind)
		}
		if strings.ContainsRune(kind, 0) {
			return filter{}, Errorf(CodeInvalidArgument, "kind must not contain NUL")
		}
		if _, duplicate := seenKinds[kind]; duplicate {
			continue
		}
		seenKinds[kind] = struct{}{}
		parsed.kinds = append(parsed.kinds, kind)
	}
	sort.Strings(parsed.kinds)

	if len(req.Severities) > 3 {
		return filter{}, Errorf(CodeInvalidArgument, "severities must contain at most 3 values")
	}
	seenSeverities := make(map[diagnostic.Severity]struct{}, len(req.Severities))
	for _, raw := range req.Severities {
		severity := diagnostic.Severity(raw)
		if !severity.Valid() {
			return filter{}, Errorf(CodeInvalidArgument,
				"severity %q is invalid; expected error, warning or info", raw)
		}
		if _, duplicate := seenSeverities[severity]; duplicate {
			continue
		}
		seenSeverities[severity] = struct{}{}
		parsed.severities = append(parsed.severities, severity)
	}
	sort.Slice(parsed.severities, func(i, j int) bool {
		return parsed.severities[i] < parsed.severities[j]
	})

	if len(req.PathPrefix) > maxFilterStringSize {
		return filter{}, Errorf(CodeInvalidArgument, "path_prefix exceeds %d bytes", maxFilterStringSize)
	}
	if strings.ContainsRune(req.PathPrefix, 0) {
		return filter{}, Errorf(CodeInvalidArgument, "path_prefix must not contain NUL")
	}
	if len(req.Function) > maxFunctionBytes {
		return filter{}, Errorf(CodeInvalidArgument, "function exceeds %d bytes", maxFunctionBytes)
	}
	if strings.ContainsRune(req.Function, 0) {
		return filter{}, Errorf(CodeInvalidArgument, "function must not contain NUL")
	}
	if req.Fingerprint != "" && !fingerprintPattern.MatchString(req.Fingerprint) {
		return filter{}, Errorf(CodeInvalidArgument,
			"fingerprint must be a sha256: digest with 64 lowercase hexadecimal characters")
	}
	if req.MinOccurrences < 0 {
		return filter{}, Errorf(CodeInvalidArgument, "min_occurrences must not be negative")
	}
	if req.MinOccurrences > maxFilterOccurrence {
		return filter{}, Errorf(CodeInvalidArgument, "min_occurrences exceeds %d", maxFilterOccurrence)
	}

	return parsed, nil
}

func (f filter) active() bool {
	return len(f.kinds) > 0 || len(f.severities) > 0 || f.pathPrefix != "" ||
		f.function != "" || f.fingerprint != "" || f.minOccurrences > 0
}

func (f filter) matches(item diagnostic.Diagnostic) bool {
	if len(f.kinds) > 0 && !containsString(f.kinds, item.Kind) {
		return false
	}
	if len(f.severities) > 0 && !containsSeverity(f.severities, item.Severity) {
		return false
	}
	if f.fingerprint != "" && item.Fingerprint != f.fingerprint {
		return false
	}
	if f.minOccurrences > 0 && item.Occurrences < f.minOccurrences {
		return false
	}
	if f.pathPrefix == "" && f.function == "" {
		return true
	}
	for _, frame := range diagnosticFrames(item) {
		if f.pathPrefix != "" && !strings.HasPrefix(frame.Path, f.pathPrefix) {
			continue
		}
		if f.function != "" && frame.Function != f.function {
			continue
		}
		return true
	}
	return false
}

type canonicalFilter struct {
	Kinds          []string `json:"kinds"`
	Severities     []string `json:"severities"`
	PathPrefix     string   `json:"path_prefix"`
	Function       string   `json:"function"`
	Fingerprint    string   `json:"fingerprint"`
	MinOccurrences int      `json:"min_occurrences"`
}

// hash identifies the filter in a cursor. A cursor cannot be reused with a
// different filter or run.
func (f filter) hash() string {
	canonical := canonicalFilter{
		Kinds:          append([]string{}, f.kinds...),
		PathPrefix:     f.pathPrefix,
		Function:       f.function,
		Fingerprint:    f.fingerprint,
		MinOccurrences: f.minOccurrences,
	}
	for _, severity := range f.severities {
		canonical.Severities = append(canonical.Severities, string(severity))
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		// A filter with only strings and ints cannot fail to marshal.
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type cursorPayload struct {
	V      int    `json:"v"`
	RunID  string `json:"run_id"`
	Offset int    `json:"offset"`
	Filter string `json:"filter"`
}

func encodeCursor(runID string, offset int, filterHash string) (string, error) {
	if offset < 0 {
		return "", Errorf(CodeInvalidArgument, "cursor offset must not be negative")
	}
	encoded, err := json.Marshal(cursorPayload{V: 1, RunID: runID, Offset: offset, Filter: filterHash})
	if err != nil {
		return "", Wrap(CodeInternal, err, "encode cursor")
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(raw, runID, filterHash string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	if len(raw) > maxCursorBytes {
		return 0, Errorf(CodeInvalidCursor, "cursor exceeds %d bytes", maxCursorBytes)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, Wrap(CodeInvalidCursor, err, "cursor is not valid base64url")
	}
	var payload cursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return 0, Wrap(CodeInvalidCursor, err, "cursor is not valid JSON")
	}
	if payload.V != 1 {
		return 0, Errorf(CodeInvalidCursor, "unsupported cursor version %d", payload.V)
	}
	if payload.RunID != runID {
		return 0, Errorf(CodeInvalidCursor, "cursor belongs to another run")
	}
	if payload.Filter != filterHash {
		return 0, Errorf(CodeInvalidCursor, "cursor was issued for different filters")
	}
	if payload.Offset < 0 {
		return 0, Errorf(CodeInvalidCursor, "cursor offset must not be negative")
	}
	return payload.Offset, nil
}

func diagnosticFrames(item diagnostic.Diagnostic) []diagnostic.Frame {
	total := len(item.Stack) + len(item.OriginStack)
	for _, context := range item.ContextStacks {
		total += len(context.Frames)
	}
	frames := make([]diagnostic.Frame, 0, total)
	frames = append(frames, item.Stack...)
	for _, context := range item.ContextStacks {
		frames = append(frames, context.Frames...)
	}
	frames = append(frames, item.OriginStack...)
	return frames
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSeverity(values []diagnostic.Severity, want diagnostic.Severity) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
