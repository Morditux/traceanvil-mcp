// Package sanitizer parses bounded AddressSanitizer, UndefinedBehaviorSanitizer,
// LeakSanitizer and ThreadSanitizer logs. It never executes a target itself.
package sanitizer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

// Tool identifies one sanitizer runtime.
type Tool string

const (
	ToolASan  Tool = "asan"
	ToolUBSan Tool = "ubsan"
	ToolLSan  Tool = "lsan"
	ToolTSan  Tool = "tsan"
)

// Valid reports whether t is supported.
func (t Tool) Valid() bool {
	switch t {
	case ToolASan, ToolUBSan, ToolLSan, ToolTSan:
		return true
	default:
		return false
	}
}

// Origin returns the canonical diagnostic origin for t.
func (t Tool) Origin() string { return "sanitizer-" + string(t) }

// ParserName returns the parser identifier stored in report metadata.
func (t Tool) ParserName() string { return "sanitizer-log-v1" }

// ParseOptions bounds an untrusted sanitizer log.
type ParseOptions struct {
	MaxBytes      int64
	MaxLines      int
	MaxIssues     int
	MaxFrames     int
	MaxStringSize int
}

const (
	DefaultMaxLogBytes   int64 = 4 << 20
	DefaultMaxLogLines         = 20000
	DefaultMaxIssues           = 2000
	DefaultMaxFrames           = 64
	DefaultMaxStringSize       = 8192
)

func (o ParseOptions) withDefaults() ParseOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxLogBytes
	}
	if o.MaxLines <= 0 {
		o.MaxLines = DefaultMaxLogLines
	}
	if o.MaxIssues <= 0 {
		o.MaxIssues = DefaultMaxIssues
	}
	if o.MaxFrames <= 0 {
		o.MaxFrames = DefaultMaxFrames
	}
	if o.MaxStringSize <= 0 {
		o.MaxStringSize = DefaultMaxStringSize
	}
	return o
}

// Issue is one parsed sanitizer error before canonical normalization.
type Issue struct {
	Kind          string
	Severity      diagnostic.Severity
	Message       string
	Stack         []diagnostic.Frame
	PreviousStack []diagnostic.Frame
	LeakedBytes   int64
	LeakedBlocks  int64
	RecordIndex   int
}

// Result is a bounded parse result. Truncated/Incomplete evidence must never
// be presented as a clean run.
type Result struct {
	Issues     []Issue
	Warnings   []string
	Truncated  bool
	Incomplete bool
	Recognized bool
}

var (
	asanHeader    = regexp.MustCompile(`ERROR: AddressSanitizer: (.+)$`)
	lsanHeader    = regexp.MustCompile(`ERROR: LeakSanitizer: detected memory leaks`)
	lsanLeak      = regexp.MustCompile(`^(Direct|Indirect|Possibly) leak of (\d+) byte\(s\) in (\d+) object\(s\)`)
	ubsanError    = regexp.MustCompile(`runtime error: (.+)$`)
	tsanHeader    = regexp.MustCompile(`WARNING: ThreadSanitizer: (.+?)(?: \(pid=\d+\))?$`)
	stackLine     = regexp.MustCompile(`^#(\d+) (?:0x[0-9a-fA-F]+ in )?(.+?)(?: \((.+?)\))?$`)
	location      = regexp.MustCompile(`^(.*?):(\d+)(?::(\d+))?$`)
	frameLocation = regexp.MustCompile(`^(?:(.*?)\s+)?([^\s:]+):(\d+)(?::(\d+))?$`)
)

// Parse consumes one sanitizer log. It is line-oriented and bounded before
// retaining strings or frames.
func Parse(tool Tool, r io.Reader, opts ParseOptions) (Result, error) {
	if r == nil {
		return Result{}, errors.New("sanitizer: nil log reader")
	}
	if !tool.Valid() {
		return Result{}, fmt.Errorf("sanitizer: unsupported tool %q", tool)
	}
	opts = opts.withDefaults()
	reader := bufio.NewReader(io.LimitReader(r, opts.MaxBytes+1))
	result := Result{Issues: []Issue{}, Warnings: []string{}}
	lines := make([]string, 0, 128)
	var totalBytes int64
	for {
		line, err := reader.ReadString('\n')
		totalBytes += int64(len(line))
		if totalBytes > opts.MaxBytes {
			result.Truncated = true
			result.Incomplete = true
			result.Warnings = append(result.Warnings, "sanitizer log exceeded the byte bound")
			break
		}
		if len(line) > opts.MaxStringSize {
			result.Truncated = true
			result.Incomplete = true
			result.Warnings = append(result.Warnings, "sanitizer log contains an oversized line")
			break
		}
		if len(lines) >= opts.MaxLines {
			result.Truncated = true
			result.Incomplete = true
			result.Warnings = append(result.Warnings, "sanitizer log exceeded the line bound")
			break
		}
		if line != "" {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return Result{}, fmt.Errorf("sanitizer: read log: %w", err)
			}
			break
		}
	}
	parseLines(tool, lines, opts, &result)
	if len(result.Issues) > opts.MaxIssues {
		result.Issues = result.Issues[:opts.MaxIssues]
		result.Truncated = true
		result.Incomplete = true
		result.Warnings = append(result.Warnings, "sanitizer issues were truncated at the collection bound")
	}
	return result, nil
}

func parseLines(tool Tool, lines []string, opts ParseOptions, result *Result) {
	unrecognized := false
	unrecognizedLine := ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		switch tool {
		case ToolASan:
			if matches := asanHeader.FindStringSubmatch(line); matches != nil {
				result.Recognized = true
				rawKind := strings.TrimSpace(matches[1])
				if index := strings.Index(rawKind, " on address"); index >= 0 {
					rawKind = rawKind[:index]
				}
				kind := "AddressSanitizer/" + truncate(rawKind, 128)
				message := line
				stack, next := collectStack(lines, i+1, opts)
				issue := Issue{Kind: kind, Severity: diagnostic.SeverityError, Message: message, Stack: stack, RecordIndex: i}
				result.Issues = append(result.Issues, issue)
				i = next - 1
				continue
			}
			if sanitizerNotice(line) {
				unrecognized = true
				unrecognizedLine = line
			}
		case ToolLSan:
			if lsanHeader.MatchString(line) {
				result.Recognized = true
				continue
			}
			if matches := lsanLeak.FindStringSubmatch(line); matches != nil {
				result.Recognized = true
				bytes, _ := strconv.ParseInt(matches[2], 10, 64)
				blocks, _ := strconv.ParseInt(matches[3], 10, 64)
				kind := "Leak_" + matches[1]
				stack, next := collectStack(lines, i+1, opts)
				result.Issues = append(result.Issues, Issue{
					Kind: kind, Severity: diagnostic.SeverityError, Message: line,
					Stack: stack, LeakedBytes: bytes, LeakedBlocks: blocks, RecordIndex: i,
				})
				i = next - 1
				continue
			}
			if sanitizerNotice(line) {
				unrecognized = true
				unrecognizedLine = line
			}
		case ToolUBSan:
			if matches := ubsanError.FindStringSubmatch(line); matches != nil {
				result.Recognized = true
				message := strings.TrimSpace(matches[1])
				locationText := strings.TrimSpace(strings.SplitN(line, ": runtime error:", 2)[0])
				frames := []diagnostic.Frame{}
				if loc := location.FindStringSubmatch(locationText); loc != nil {
					lineNo, _ := strconv.Atoi(loc[2])
					frames = append(frames, diagnostic.Frame{Path: strings.TrimSpace(loc[1]), Line: lineNo})
				}
				stack, next := collectStack(lines, i+1, opts)
				if len(stack) > 0 {
					frames = stack
				}
				result.Issues = append(result.Issues, Issue{
					Kind: "UndefinedBehavior", Severity: diagnostic.SeverityError,
					Message: message, Stack: frames, RecordIndex: i,
				})
				i = next - 1
				continue
			}
		case ToolTSan:
			if matches := tsanHeader.FindStringSubmatch(line); matches != nil {
				result.Recognized = true
				rawKind := strings.TrimSpace(matches[1])
				kind := tsanKind(rawKind)
				stack, next := collectStack(lines, i+1, opts)
				previous, _ := collectPreviousStack(lines, next, opts)
				result.Issues = append(result.Issues, Issue{
					Kind: kind, Severity: diagnostic.SeverityError, Message: line,
					Stack: stack, PreviousStack: previous, RecordIndex: i,
				})
				i = next - 1
				continue
			}
			if sanitizerNotice(line) {
				unrecognized = true
				unrecognizedLine = line
			}
		}
	}
	if unrecognized {
		// A sanitizer-looking line that was not parsed is not evidence of a
		// clean run. This also catches runtime initialization failures.
		result.Incomplete = true
		warning := "sanitizer output contained unrecognized diagnostics"
		if unrecognizedLine != "" {
			warning += ": " + truncate(unrecognizedLine, 200)
		}
		result.Warnings = append(result.Warnings, warning)
	}
	if result.Recognized && len(result.Issues) == 0 {
		result.Incomplete = true
		result.Warnings = append(result.Warnings, "sanitizer reported an issue header without a parsed diagnostic")
	}
	if !result.Recognized && len(result.Issues) == 0 && len(lines) > 0 {
		// Unknown output is not proof of a clean run.
		result.Incomplete = true
		result.Warnings = append(result.Warnings, "no recognized sanitizer diagnostics were found in a non-empty log")
	}
}

func collectStack(lines []string, start int, opts ParseOptions) ([]diagnostic.Frame, int) {
	frames := []diagnostic.Frame{}
	index := start
	// ASan and TSan put a "READ of size" / "Previous write" preamble between
	// the header and the first frame. Skip a small, bounded lead-in and stop at
	// the next error rather than folding several reports into one stack.
	for index < len(lines) && index < start+16 {
		line := strings.TrimSpace(lines[index])
		if strings.HasPrefix(line, "#") {
			break
		}
		if line == "" {
			index++
			continue
		}
		if sanitizerBoundary(line) {
			return frames, index
		}
		index++
	}
	for index < len(lines) && len(frames) < opts.MaxFrames {
		line := strings.TrimSpace(lines[index])
		if line == "" || !strings.HasPrefix(line, "#") {
			break
		}
		if matches := stackLine.FindStringSubmatch(line); matches != nil {
			text := strings.TrimSpace(matches[2])
			object := strings.TrimSpace(matches[3])
			if strings.HasPrefix(text, "0x") {
				// Unsymbolized frames can have no "in" marker. Never retain the
				// ASLR-randomized instruction address in a fingerprint.
				fields := strings.Fields(text)
				if len(fields) > 0 {
					text = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
				}
			}
			if object == "" {
				// Some runtime formats append several parenthetical annotations
				// after the symbol; do not treat them as part of the function.
				if index := strings.Index(text, " ("); index >= 0 {
					text = strings.TrimSpace(text[:index])
				}
			}
			if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
				object = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "("), ")"))
				text = ""
			}
			frame := diagnostic.Frame{Function: text, Object: object}
			if locationMatches := frameLocation.FindStringSubmatch(text); locationMatches != nil {
				frame.Function = strings.TrimSpace(locationMatches[1])
				frame.Path = strings.TrimSpace(locationMatches[2])
				frame.Line, _ = strconv.Atoi(locationMatches[3])
			} else if locationMatches := location.FindStringSubmatch(text); locationMatches != nil {
				frame.Path = strings.TrimSpace(locationMatches[1])
				frame.Line, _ = strconv.Atoi(locationMatches[2])
			}
			if frame.Function != "" || frame.Path != "" || frame.Object != "" {
				frames = append(frames, frame)
			}
		}
		index++
	}
	return frames, index
}

func collectPreviousStack(lines []string, start int, opts ParseOptions) ([]diagnostic.Frame, int) {
	for index := start; index < len(lines) && index < start+32; index++ {
		line := strings.TrimSpace(lines[index])
		if strings.HasPrefix(line, "Previous ") {
			return collectStack(lines, index+1, opts)
		}
		if sanitizerBoundary(line) {
			break
		}
	}
	return nil, start
}

func sanitizerNotice(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "#"),
		strings.HasPrefix(line, "SUMMARY:"),
		strings.HasPrefix(line, "allocated by "),
		strings.HasPrefix(line, "Shadow "),
		strings.HasPrefix(line, "0x"),
		strings.HasPrefix(line, "=>0x"):
		return false
	case strings.HasPrefix(line, "ThreadSanitizer: reported "):
		return false
	case strings.HasPrefix(line, "AddressSanitizer:"),
		strings.HasPrefix(line, "LeakSanitizer:"),
		strings.HasPrefix(line, "ThreadSanitizer:"),
		strings.HasPrefix(line, "UndefinedBehaviorSanitizer:"):
		return true
	}
	return strings.Contains(line, "ERROR: AddressSanitizer") ||
		strings.Contains(line, "ERROR: LeakSanitizer") ||
		strings.Contains(line, "WARNING: ThreadSanitizer")
}

func sanitizerBoundary(line string) bool {
	return strings.HasPrefix(line, "ERROR: ") ||
		strings.HasPrefix(line, "<empty stack>") ||
		lsanLeak.MatchString(line) ||
		strings.HasPrefix(line, "WARNING: ThreadSanitizer:") ||
		strings.HasPrefix(line, "SUMMARY: ") ||
		strings.Contains(line, "runtime error:")
}

func tsanKind(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "data race"):
		return "DataRace"
	case strings.Contains(lower, "lock-order-inversion"):
		return "LockOrderInversion"
	case strings.Contains(lower, "mutex"):
		return "Mutex"
	case strings.Contains(lower, "thread leak"):
		return "ThreadLeak"
	default:
		return "ThreadSanitizer/" + truncate(raw, 96)
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
