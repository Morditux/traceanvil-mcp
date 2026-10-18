package capabilities

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Registry owns the current capability records. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	probes  []Probe
	records map[string]Capability
	timeout time.Duration
}

// NewRegistry returns a registry that runs the given probes on Refresh.
func NewRegistry(probes []Probe, timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = probeTimeout
	}
	return &Registry{
		probes:  append([]Probe(nil), probes...),
		records: make(map[string]Capability, len(probes)),
		timeout: timeout,
	}
}

// Refresh runs every probe with its own timeout and stores the results.
// Probes run concurrently; a slow or panicking probe cannot block or crash the
// server. The returned records are ordered by capability ID.
func (r *Registry) Refresh(ctx context.Context) []Capability {
	results := make([]Capability, len(r.probes))

	var wg sync.WaitGroup
	for i, probe := range r.probes {
		wg.Add(1)
		go func(i int, probe Probe) {
			defer wg.Done()
			results[i] = r.runProbe(ctx, probe)
		}(i, probe)
	}
	wg.Wait()

	r.mu.Lock()
	for _, record := range results {
		r.records[record.ID] = cloneCapability(record)
	}
	r.mu.Unlock()

	return sortCapabilities(results)
}

func (r *Registry) runProbe(ctx context.Context, probe Probe) Capability {
	start := time.Now()
	record := Capability{
		ID:          probe.ID(),
		Kind:        "backend",
		Backend:     probe.Backend(),
		Features:    probe.Features(),
		State:       StateUnknown,
		LastChecked: time.Now().UTC().Format(time.RFC3339),
		Probe: ProbeResult{
			Attempted: true,
		},
	}

	probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Run the probe in its own goroutine so a probe that ignores cancellation
	// cannot block server startup. Well-behaved probes honor probeCtx; a
	// misbehaving one leaks its goroutine but the registry still returns.
	done := make(chan Result, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- Result{
					State:  StateUnknown,
					Reason: "probe panicked",
					Detail: "panic recovered",
					OK:     false,
				}
			}
		}()
		done <- probe.Run(probeCtx)
	}()

	var result Result
	select {
	case result = <-done:
	case <-probeCtx.Done():
	}

	// A probe may return a result at the same instant its deadline expires; the
	// select can then choose either branch. Never trust a result once the probe
	// context is done: a late "available" must not publish a capability.
	if err := probeCtx.Err(); err != nil {
		result = Result{State: StateUnknown, OK: false}
		if errors.Is(err, context.DeadlineExceeded) {
			result.Reason = "probe timed out"
			result.Detail = "probe did not return before the deadline"
		} else {
			result.Reason = "probe canceled"
		}
	}

	if result.State == "" {
		result.State = StateUnknown
	}
	if !result.State.Valid() {
		result.State = StateUnknown
		result.Reason = "probe returned an invalid state"
		result.OK = false
	}

	record.State = result.State
	record.Path = result.Path
	record.Version = result.Version
	record.Reason = result.Reason
	record.Probe.Success = result.OK
	record.Probe.Detail = result.Detail
	record.Probe.DurationMS = time.Since(start).Milliseconds()
	return record
}

// Snapshot returns a copy of the current records, ordered by ID.
func (r *Registry) Snapshot() []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()

	records := make([]Capability, 0, len(r.records))
	for _, record := range r.records {
		records = append(records, cloneCapability(record))
	}
	return sortCapabilities(records)
}

// Get returns one record.
func (r *Registry) Get(id string) (Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.records[id]
	if !ok {
		return Capability{}, false
	}
	return cloneCapability(record), true
}

// State returns the state of one capability, or StateUnknown if it is not
// registered.
func (r *Registry) State(id string) State {
	record, ok := r.Get(id)
	if !ok {
		return StateUnknown
	}
	return record.State
}

// Set replaces one record. It exists for administrative refreshes and tests;
// runtime callers must validate the record first.
func (r *Registry) Set(record Capability) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if record.LastChecked == "" {
		record.LastChecked = time.Now().UTC().Format(time.RFC3339)
	}

	r.mu.Lock()
	r.records[record.ID] = cloneCapability(record)
	r.mu.Unlock()
	return nil
}

func cloneCapability(record Capability) Capability {
	record.Features = append([]string(nil), record.Features...)
	return record
}

func sortCapabilities(records []Capability) []Capability {
	sorted := append([]Capability(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted
}
