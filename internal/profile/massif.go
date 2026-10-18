package profile

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// MassifResult is the parsed native Massif profile.
type MassifResult struct {
	TimeUnit     string
	Snapshots    int
	PeakHeap     uint64
	PeakExtra    uint64
	PeakStacks   uint64
	Samples      []MassifSnapshot
	PeakIndex    int
	PeakSnapshot MassifSnapshot
	Lines        int
	Warnings     []string
}

type massifParser struct {
	timeUnit    string
	current     MassifSnapshot
	inSnapshot  bool
	snapshots   int
	peakHeap    uint64
	peakExtra   uint64
	peakStacks  uint64
	peak        MassifSnapshot
	peakIndex   int
	peakOrdinal int
	peakMarked  bool
	markedPeak  MassifSnapshot
	samples     []MassifSnapshot
	lines       int
	warnings    []string
}

// ParseMassif parses the native Massif output. Heap-tree text is deliberately
// not interpreted: only the bounded snapshot fields are metrics. The peak is
// the snapshot with the largest mem_heap_B; heap_tree=peak is retained as a
// cross-check and a mismatch is reported instead of silently choosing one.
func ParseMassif(r io.Reader) (*MassifResult, error) {
	if r == nil {
		return nil, Errorf(CodeInvalidArgument, "nil Massif reader")
	}
	p := &massifParser{peakIndex: -1, peakOrdinal: -1}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxLineBytes)
	for scanner.Scan() {
		p.lines++
		if p.lines > MaxLines {
			return nil, Errorf(CodeProfileIncomplete, "Massif profile exceeds %d lines", MaxLines)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := p.parseLine(line); err != nil {
			return nil, err
		}
	}
	if err := p.finishSnapshot(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, Wrap(CodeProfileIncomplete, err, "read Massif profile")
	}
	if p.snapshots == 0 {
		return nil, Errorf(CodeProfileIncomplete, "Massif profile has no snapshots")
	}
	if p.timeUnit == "" {
		p.warnings = append(p.warnings, "Massif profile has no time_unit; comparison is unavailable")
	}
	if p.peakMarked && p.markedPeak.Index != p.peak.Index {
		p.warnings = append(p.warnings, "Massif heap_tree=peak and maximum mem_heap_B disagree; maximum heap is authoritative")
	}
	p.finalizeSamples()
	return &MassifResult{
		TimeUnit: p.timeUnit, Snapshots: p.snapshots,
		PeakHeap: p.peakHeap, PeakExtra: p.peakExtra, PeakStacks: p.peakStacks,
		Samples: p.samples, PeakIndex: p.peak.Index, PeakSnapshot: p.peak,
		Lines: p.lines, Warnings: p.warnings,
	}, nil
}

func (p *massifParser) parseLine(line string) error {
	switch {
	case strings.HasPrefix(line, "time_unit:"):
		value := strings.TrimSpace(strings.TrimPrefix(line, "time_unit:"))
		if value == "" {
			return Errorf(CodeProfileIncomplete, "empty Massif time_unit")
		}
		if p.timeUnit != "" && p.timeUnit != value {
			return Errorf(CodeProfileIncomplete, "Massif time_unit changes within the profile")
		}
		p.timeUnit = value
		return nil
	case strings.HasPrefix(line, "snapshot="):
		if err := p.finishSnapshot(); err != nil {
			return err
		}
		index, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "snapshot=")))
		if err != nil || index < 0 {
			return Errorf(CodeProfileIncomplete, "invalid Massif snapshot index")
		}
		p.current = MassifSnapshot{Index: index}
		p.inSnapshot = true
		p.snapshots++
		if p.snapshots > MaxSnapshotCount {
			return Errorf(CodeProfileIncomplete, "Massif profile exceeds %d snapshots", MaxSnapshotCount)
		}
		return nil
	case strings.HasPrefix(line, "time="):
		if !p.inSnapshot {
			return Errorf(CodeProfileIncomplete, "Massif time appears before snapshot")
		}
		value, err := parseUintField(strings.TrimPrefix(line, "time="))
		if err != nil {
			return err
		}
		p.current.Time = value
		return nil
	case strings.HasPrefix(line, "mem_heap_B="):
		if !p.inSnapshot {
			return Errorf(CodeProfileIncomplete, "Massif mem_heap_B appears before snapshot")
		}
		value, err := parseUintField(strings.TrimPrefix(line, "mem_heap_B="))
		if err != nil {
			return err
		}
		p.current.HeapBytes = value
		return nil
	case strings.HasPrefix(line, "mem_heap_extra_B="):
		if !p.inSnapshot {
			return Errorf(CodeProfileIncomplete, "Massif mem_heap_extra_B appears before snapshot")
		}
		value, err := parseUintField(strings.TrimPrefix(line, "mem_heap_extra_B="))
		if err != nil {
			return err
		}
		p.current.HeapExtra = value
		return nil
	case strings.HasPrefix(line, "mem_stacks_B="):
		if !p.inSnapshot {
			return Errorf(CodeProfileIncomplete, "Massif mem_stacks_B appears before snapshot")
		}
		value, err := parseUintField(strings.TrimPrefix(line, "mem_stacks_B="))
		if err != nil {
			return err
		}
		p.current.StacksBytes = value
		return nil
	case strings.HasPrefix(line, "heap_tree="):
		value := strings.TrimSpace(strings.TrimPrefix(line, "heap_tree="))
		if value == "peak" && p.inSnapshot {
			p.current.Peak = true
			p.peakMarked = true
			p.markedPeak = p.current
		}
		return nil
	default:
		// desc/cmd and heap-tree lines are metadata, not metrics.
		return nil
	}
}

func (p *massifParser) finishSnapshot() error {
	if !p.inSnapshot {
		return nil
	}
	ordinal := p.snapshots - 1
	if p.peakIndex < 0 || p.current.HeapBytes > p.peakHeap {
		p.peak = p.current
		p.peakHeap = p.current.HeapBytes
		p.peakIndex = ordinal
	}
	if p.current.Peak && p.current.HeapBytes >= p.peakHeap {
		p.peak = p.current
		p.peakHeap = p.current.HeapBytes
		p.peakIndex = ordinal
	}
	if p.current.HeapExtra > p.peakExtra {
		p.peakExtra = p.current.HeapExtra
	}
	if p.current.StacksBytes > p.peakStacks {
		p.peakStacks = p.current.StacksBytes
	}
	if len(p.samples) < MaxMassifSnapshots {
		p.samples = append(p.samples, p.current)
	} else if ordinal == p.peakIndex {
		// Preserve the peak even when the bounded sample list is full.
		p.samples[len(p.samples)-1] = p.current
	}
	p.current = MassifSnapshot{}
	p.inSnapshot = false
	return nil
}

func (p *massifParser) finalizeSamples() {
	found := false
	for i := range p.samples {
		p.samples[i].Peak = false
		if p.samples[i].Index == p.peak.Index {
			p.samples[i].Peak = true
			found = true
		}
	}
	if found || len(p.samples) == 0 {
		return
	}
	peak := p.peak
	peak.Peak = true
	p.samples[len(p.samples)-1] = peak
}

func parseUintField(value string) (uint64, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, Errorf(CodeProfileIncomplete, "empty Massif numeric field")
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, Errorf(CodeProfileIncomplete, "invalid Massif numeric field %q", text)
	}
	return parsed, nil
}

func massifMetrics(result *MassifResult) map[string]float64 {
	return map[string]float64{
		"heap_peak_bytes":       float64(result.PeakHeap),
		"heap_extra_peak_bytes": float64(result.PeakExtra),
		"stack_peak_bytes":      float64(result.PeakStacks),
		"snapshots":             float64(result.Snapshots),
	}
}
