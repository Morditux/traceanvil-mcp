package profile

import (
	"bufio"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CallgrindResult is the parsed native Callgrind profile.
type CallgrindResult struct {
	Events        []string
	Totals        []float64
	TopFunctions  []FunctionCost
	FunctionCount int
	Lines         int
	Warnings      []string
}

type callgrindParser struct {
	events     []string
	totals     []float64
	summary    []float64
	hasTotals  bool
	hasSummary bool
	positions  int

	current        string
	currentFile    string
	currentObj     string
	currentFnFile  string
	currentFnObj   string
	fileSetSinceFn bool
	callFile       string
	callObj        string
	names          map[string]map[string]string // kind -> id -> name
	fnFiles        map[string]string
	fnObjs         map[string]string
	costs          map[string][]float64
	skipCost       bool
	lines          int
	warnings       []string
}

// ParseCallgrind parses the native Callgrind format with bounded memory and
// line lengths. It resolves Callgrind's (id) name compression and excludes
// inclusive call costs that follow a calls= line from self costs.
func ParseCallgrind(r io.Reader) (*CallgrindResult, error) {
	if r == nil {
		return nil, Errorf(CodeInvalidArgument, "nil Callgrind reader")
	}
	p := &callgrindParser{
		positions: 1,
		names: map[string]map[string]string{
			"fn": {}, "fl": {}, "ob": {},
		},
		fnFiles: make(map[string]string),
		fnObjs:  make(map[string]string),
		costs:   make(map[string][]float64),
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxLineBytes)
	for scanner.Scan() {
		p.lines++
		if p.lines > MaxLines {
			return nil, Errorf(CodeProfileIncomplete, "Callgrind profile exceeds %d lines", MaxLines)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := p.parseLine(line); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, Wrap(CodeProfileIncomplete, err, "read Callgrind profile")
	}
	if p.skipCost {
		return nil, Errorf(CodeProfileIncomplete, "Callgrind calls= line has no following cost")
	}
	if len(p.events) == 0 {
		return nil, Errorf(CodeProfileIncomplete, "Callgrind profile has no events header")
	}
	if len(p.events) > MaxEvents {
		return nil, Errorf(CodeProfileIncomplete, "Callgrind profile has more than %d events", MaxEvents)
	}
	totals := p.totals
	if !p.hasTotals {
		totals = p.summary
	}
	if !p.hasTotals && !p.hasSummary {
		totals = make([]float64, len(p.events))
		for _, values := range p.costs {
			for i := range values {
				totals[i] += values[i]
			}
		}
	}
	if len(totals) < len(p.events) {
		totals = append(totals, make([]float64, len(p.events)-len(totals))...)
	}
	top := make([]FunctionCost, 0, len(p.costs))
	for key, values := range p.costs {
		parts := strings.SplitN(key, "\x00", 3)
		name, file := parts[0], ""
		if len(parts) > 1 {
			file = parts[1]
		}
		if len(values) == 0 {
			continue
		}
		top = append(top, FunctionCost{
			Name: name, File: file, Event: p.events[0], Self: values[0],
		})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Self != top[j].Self {
			return top[i].Self > top[j].Self
		}
		if top[i].Name != top[j].Name {
			return top[i].Name < top[j].Name
		}
		return top[i].File < top[j].File
	})
	if len(top) > MaxTopFunctions {
		top = top[:MaxTopFunctions]
	}
	return &CallgrindResult{
		Events:       append([]string(nil), p.events...),
		Totals:       append([]float64(nil), totals...),
		TopFunctions: top, FunctionCount: len(p.costs),
		Lines: p.lines, Warnings: p.warnings,
	}, nil
}

func (p *callgrindParser) parseLine(line string) error {
	switch {
	case strings.HasPrefix(line, "events:"):
		names := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "events:")))
		if len(names) == 0 {
			return Errorf(CodeProfileIncomplete, "empty Callgrind events header")
		}
		if len(names) > MaxEvents {
			return Errorf(CodeProfileIncomplete, "Callgrind events header exceeds %d entries", MaxEvents)
		}
		p.events = append([]string(nil), names...)
		return nil
	case strings.HasPrefix(line, "positions:"):
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "positions:")))
		if len(fields) == 0 {
			return Errorf(CodeProfileIncomplete, "empty Callgrind positions header")
		}
		if len(fields) > 8 {
			return Errorf(CodeProfileIncomplete, "Callgrind positions header is too large")
		}
		p.positions = len(fields)
		return nil
	case strings.HasPrefix(line, "summary:"):
		values, err := parseCosts(strings.TrimSpace(strings.TrimPrefix(line, "summary:")), len(p.events))
		if err != nil {
			return err
		}
		p.summary, p.hasSummary = values, true
		return nil
	case strings.HasPrefix(line, "totals:"):
		values, err := parseCosts(strings.TrimSpace(strings.TrimPrefix(line, "totals:")), len(p.events))
		if err != nil {
			return err
		}
		p.totals, p.hasTotals = values, true
		return nil
	case strings.HasPrefix(line, "fn="):
		raw := strings.TrimSpace(strings.TrimPrefix(line, "fn="))
		id, name, hasID := callgrindRef(raw)
		if hasID {
			if name != "" {
				p.names["fn"][id] = cleanCallgrindName(name)
				// Use the current fl/fi/fe context for a newly named function;
				// currentFnFile still belongs to the previous function.
				p.currentFnFile = p.currentFile
				p.currentFnObj = p.currentObj
				p.fnFiles[id] = p.currentFnFile
				p.fnObjs[id] = p.currentFnObj
				p.current = p.names["fn"][id]
				p.fileSetSinceFn = false
				return nil
			}
			resolved, ok := p.names["fn"][id]
			if !ok {
				return Errorf(CodeProfileIncomplete, "Callgrind fn id (%s) is not defined", id)
			}
			p.current = resolved
			if p.fileSetSinceFn {
				p.fnFiles[id] = p.currentFile
				p.fnObjs[id] = p.currentObj
			}
			if file := p.fnFiles[id]; file != "" {
				p.currentFnFile = file
			}
			if obj := p.fnObjs[id]; obj != "" {
				p.currentFnObj = obj
			}
			p.fileSetSinceFn = false
			return nil
		}
		p.current = cleanCallgrindName(raw)
		p.currentFnFile = p.currentFile
		p.currentFnObj = p.currentObj
		p.fileSetSinceFn = false
		return nil
	case strings.HasPrefix(line, "fl="):
		name, err := p.resolve("fl", strings.TrimSpace(strings.TrimPrefix(line, "fl=")))
		if err != nil {
			return err
		}
		p.currentFile = name
		p.currentFnFile = name
		p.fileSetSinceFn = true
		return nil
	case strings.HasPrefix(line, "fi="), strings.HasPrefix(line, "fe="):
		// Inline-file transitions do not change the defining file of the
		// enclosing function.
		name, err := p.resolve("fl", strings.TrimSpace(line[3:]))
		if err != nil {
			return err
		}
		p.currentFile = name
		return nil
	case strings.HasPrefix(line, "ob="):
		name, err := p.resolve("ob", strings.TrimSpace(strings.TrimPrefix(line, "ob=")))
		if err != nil {
			return err
		}
		p.currentObj = name
		p.currentFnObj = name
		return nil
	case strings.HasPrefix(line, "cfn="):
		// cfl/cfi/cfn describe a call target; remember its defining file for
		// later bare fn=(id) blocks but never add the inclusive call cost to
		// the caller's self cost.
		id, name, hasID := callgrindRef(strings.TrimSpace(strings.TrimPrefix(line, "cfn=")))
		if !hasID {
			return Errorf(CodeProfileIncomplete, "Callgrind cfn has no id")
		}
		resolved, err := p.resolve("fn", strings.TrimSpace(strings.TrimPrefix(line, "cfn=")))
		if err != nil {
			return err
		}
		if name != "" {
			p.names["fn"][id] = resolved
			file := p.callFile
			if file == "" {
				file = p.currentFile
			}
			p.fnFiles[id] = file
			obj := p.callObj
			if obj == "" {
				obj = p.currentObj
			}
			p.fnObjs[id] = obj
		}
		return nil
	case strings.HasPrefix(line, "cfl="), strings.HasPrefix(line, "cfi="):
		name, err := p.resolve("fl", strings.TrimSpace(line[4:]))
		if err != nil {
			return err
		}
		p.callFile = name
		return nil
	case strings.HasPrefix(line, "cob="):
		name, err := p.resolve("ob", strings.TrimSpace(strings.TrimPrefix(line, "cob=")))
		if err != nil {
			return err
		}
		p.callObj = name
		return nil
	case strings.HasPrefix(line, "calls="):
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "calls=")))
		if len(fields) == 0 {
			return Errorf(CodeProfileIncomplete, "empty Callgrind calls= line")
		}
		count, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || count == 0 {
			return Errorf(CodeProfileIncomplete, "invalid Callgrind calls count")
		}
		// cfl/cob describe this call only; Callgrind omits them when the
		// callee shares the caller's file/object.
		p.callFile = ""
		p.callObj = ""
		p.skipCost = true
		return nil
	case strings.HasPrefix(line, "jfn="), strings.HasPrefix(line, "jump="),
		strings.HasPrefix(line, "jcnd="):
		return nil
	case strings.HasPrefix(line, "version:"), strings.HasPrefix(line, "creator:"),
		strings.HasPrefix(line, "pid:"), strings.HasPrefix(line, "cmd:"),
		strings.HasPrefix(line, "part:"), strings.HasPrefix(line, "desc:"),
		strings.HasPrefix(line, "thread:"):
		return nil
	default:
		if isCostLine(line) {
			return p.parseCostLine(line)
		}
		return nil
	}
}

func (p *callgrindParser) parseCostLine(line string) error {
	if p.skipCost {
		// This is the inclusive cost of the preceding call. It must not be
		// attributed to the caller's self cost.
		p.skipCost = false
		return nil
	}
	if p.current == "" {
		return Errorf(CodeProfileIncomplete, "Callgrind cost line appears before fn=")
	}
	if len(p.events) == 0 {
		return Errorf(CodeProfileIncomplete, "Callgrind cost line appears before events header")
	}
	fields := strings.Fields(line)
	if len(fields) < p.positions+1 {
		return Errorf(CodeProfileIncomplete, "Callgrind cost line is shorter than its positions")
	}
	values := make([]float64, len(p.events))
	for i := range values {
		index := p.positions + i
		if index >= len(fields) {
			return Errorf(CodeProfileIncomplete, "Callgrind cost line has fewer values than events")
		}
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return Errorf(CodeProfileIncomplete, "invalid Callgrind cost %q", fields[index])
		}
		values[i] = value
	}
	key := p.current + "\x00" + p.currentFnFile + "\x00" + p.currentFnObj
	existing, ok := p.costs[key]
	if !ok {
		if len(p.costs) >= MaxFunctions {
			return Errorf(CodeProfileIncomplete, "Callgrind profile exceeds %d functions", MaxFunctions)
		}
		existing = make([]float64, len(p.events))
		p.costs[key] = existing
	}
	for i, value := range values {
		existing[i] += value
	}
	return nil
}

func callgrindRef(raw string) (id, name string, hasID bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "(") {
		return "", raw, false
	}
	end := strings.Index(raw, ")")
	if end < 0 {
		return "", "", true
	}
	return raw[1:end], strings.TrimSpace(raw[end+1:]), true
}

func (p *callgrindParser) resolve(kind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", Errorf(CodeProfileIncomplete, "empty Callgrind %s name", kind)
	}
	id, name, hasID := callgrindRef(raw)
	if !hasID {
		return cleanCallgrindName(raw), nil
	}
	if id == "" {
		return "", Errorf(CodeProfileIncomplete, "malformed Callgrind %s id", kind)
	}
	if name != "" {
		name = cleanCallgrindName(name)
		p.names[kind][id] = name
		return name, nil
	}
	name, ok := p.names[kind][id]
	if !ok {
		return "", Errorf(CodeProfileIncomplete, "Callgrind %s id (%s) is not defined", kind, id)
	}
	return name, nil
}

func isCostLine(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	return (c >= '0' && c <= '9') || c == '+' || c == '*' || c == '-'
}

func parseCosts(text string, events int) ([]float64, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || events == 0 {
		return nil, Errorf(CodeProfileIncomplete, "empty Callgrind totals")
	}
	values := make([]float64, events)
	for i := range values {
		if i >= len(fields) {
			return nil, Errorf(CodeProfileIncomplete, "Callgrind totals have fewer values than events")
		}
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, Errorf(CodeProfileIncomplete, "invalid Callgrind total %q", fields[i])
		}
		values[i] = value
	}
	return values, nil
}

func cleanCallgrindName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = truncateUTF8(value, 256)
	}
	return value
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func formatEventMetrics(events []string, totals []float64) []EventMetric {
	out := make([]EventMetric, 0, len(events))
	for i, name := range events {
		total := 0.0
		if i < len(totals) {
			total = totals[i]
		}
		out = append(out, EventMetric{Name: name, Total: total})
	}
	return out
}

func callgrindMetrics(result *CallgrindResult) map[string]float64 {
	metrics := make(map[string]float64, len(result.Events)+2)
	for i, event := range result.Events {
		if i < len(result.Totals) {
			metrics["event_"+sanitizeMetricName(event)] = result.Totals[i]
		}
	}
	if len(result.Events) > 0 && len(result.Totals) > 0 {
		metrics["total"] = result.Totals[0]
	}
	metrics["functions"] = float64(result.FunctionCount)
	return metrics
}

func sanitizeMetricName(name string) string {
	var b strings.Builder
	for _, r := range name {
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
