package perf

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
)

// Parsed is the normalized result of perf stat JSON output.
type Parsed struct {
	Events   []EventMetric
	Warnings []string
	Lines    int
}

const maxPerfLine = 1 << 20

// ParseStat parses perf stat -j lines. Unsupported/not-counted counters are
// surfaced as warnings and do not become fabricated zero metrics.
func ParseStat(r io.Reader) (*Parsed, error) {
	if r == nil {
		return nil, Errorf(CodeInvalidArgument, "nil perf reader")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxPerfLine)
	result := &Parsed{Events: []EventMetric{}, Warnings: []string{}}
	seen := make(map[string]int)
	for scanner.Scan() {
		result.Lines++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var raw struct {
			Event        string          `json:"event"`
			CounterValue json.RawMessage `json:"counter-value"`
			Unit         string          `json:"unit"`
			Running      json.RawMessage `json:"pcnt-running"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, Wrap(CodeProfileIncomplete, err, "decode perf stat JSON line")
		}
		if raw.Event == "" {
			continue
		}
		text := rawCounter(raw.CounterValue)
		if text == "" || strings.Contains(text, "not counted") || strings.Contains(text, "not supported") {
			result.Warnings = append(result.Warnings,
				"perf event "+raw.Event+" was not counted: "+text)
			continue
		}
		value, err := parsePerfValue(text)
		if err != nil {
			result.Warnings = append(result.Warnings,
				"perf event "+raw.Event+" has an unparsable value: "+text)
			continue
		}
		running := 100.0
		if runningText := rawCounter(raw.Running); runningText != "" {
			if parsed, err := parsePerfValue(runningText); err == nil {
				running = parsed
			} else {
				running = 0
				result.Warnings = append(result.Warnings,
					"perf event "+raw.Event+" has an unparsable pcnt-running value: "+runningText)
			}
		}
		if index, ok := seen[raw.Event]; ok {
			result.Events[index].Value += value
			continue
		}
		seen[raw.Event] = len(result.Events)
		result.Events = append(result.Events, EventMetric{Name: raw.Event, Value: value, Unit: raw.Unit, RunningPercent: running})
	}
	if err := scanner.Err(); err != nil {
		return nil, Wrap(CodeProfileIncomplete, err, "read perf stat output")
	}
	if len(result.Events) == 0 {
		return nil, Errorf(CodeProfileIncomplete, "perf stat produced no numeric counters")
	}
	return result, nil
}

func rawCounter(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if err := json.Unmarshal(value, &number); err == nil {
		return number.String()
	}
	return strings.TrimSpace(string(value))
}

func parsePerfValue(text string) (float64, error) {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, ",", "")
	text = strings.TrimSuffix(text, "%")
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, Errorf(CodeProfileIncomplete, "invalid perf counter %q", text)
	}
	return value, nil
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "event"
	}
	return b.String()
}
