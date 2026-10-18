package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// RetentionPolicy bounds artifact runs by age, count and total bytes. A zero
// field disables that particular bound. The policy is applied by an explicit
// purge or by an explicitly configured startup purge; it never runs
// implicitly inside a normal tool call.
type RetentionPolicy struct {
	MaxAge   time.Duration
	MaxRuns  int
	MaxBytes int64
}

// PurgeResult is a deterministic summary of a retention pass.
type PurgeResult struct {
	DryRun       bool     `json:"dry_run"`
	Removed      []string `json:"removed"`
	Kept         int      `json:"kept"`
	RemovedBytes int64    `json:"removed_bytes"`
	KeptBytes    int64    `json:"kept_bytes"`
	Warnings     []string `json:"warnings,omitempty"`
}

type retentionCandidate struct {
	id        string
	path      string
	modTime   time.Time
	size      int64
	protected bool
}

const incompleteRunGrace = time.Hour

// Purge removes run directories older than the policy, then the oldest runs
// until the count and byte bounds hold. Only direct child directories whose
// name matches the store's opaque run-id pattern and that are owned by the
// current user are considered. Symlinks and non-run entries are never followed
// or removed. The caller must stop the server first; a same-user actor can
// still race the filesystem, so this is not a sandbox.
func (s *Store) Purge(ctx context.Context, policy RetentionPolicy, dryRun bool) (PurgeResult, error) {
	if ctx == nil {
		return PurgeResult{}, errors.New("artifacts: nil purge context")
	}
	if policy.MaxAge < 0 || policy.MaxRuns < 0 || policy.MaxBytes < 0 {
		return PurgeResult{}, errors.New("artifacts: retention values must not be negative")
	}
	result := PurgeResult{DryRun: dryRun, Removed: []string{}, Warnings: []string{}}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return result, fmt.Errorf("artifacts: list retention root: %w", err)
	}
	var candidates []retentionCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := entry.Name()
		if !runIDPattern.MatchString(name) {
			continue
		}
		path := filepath.Join(s.root, name)
		info, err := os.Lstat(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("inspect %s: %v", name, err))
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip non-directory run entry %s", name))
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip run %s owned by another user", name))
			continue
		}
		size, err := directorySize(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("measure %s: %v", name, err))
			continue
		}
		protected := false
		if _, err := os.Stat(filepath.Join(path, "result.json")); errors.Is(err, os.ErrNotExist) &&
			time.Since(info.ModTime()) < incompleteRunGrace {
			protected = true
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("keep recent incomplete run %s (no result.json)", name))
		}
		candidates = append(candidates, retentionCandidate{id: name, path: path, modTime: info.ModTime(), size: size, protected: protected})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].modTime.Before(candidates[j].modTime)
	})
	expired := make(map[string]bool)
	if policy.MaxAge > 0 {
		cutoff := time.Now().Add(-policy.MaxAge)
		for _, candidate := range candidates {
			if candidate.modTime.Before(cutoff) {
				expired[candidate.id] = true
			}
		}
	}
	var keep []retentionCandidate
	var remove []retentionCandidate
	keptCount := 0
	var keptBytes int64
	for _, candidate := range candidates {
		if expired[candidate.id] && !candidate.protected {
			remove = append(remove, candidate)
			continue
		}
		keep = append(keep, candidate)
		keptCount++
		keptBytes += candidate.size
	}
	// Keep the newest run even when a single run exceeds a count/byte bound;
	// deleting the evidence just produced is worse than reporting an overage.
	for len(keep) > 1 && ((policy.MaxRuns > 0 && keptCount > policy.MaxRuns) ||
		(policy.MaxBytes > 0 && keptBytes > policy.MaxBytes)) {
		index := -1
		for i, candidate := range keep[:len(keep)-1] {
			if !candidate.protected {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		candidate := keep[index]
		keep = append(keep[:index], keep[index+1:]...)
		keptCount--
		keptBytes -= candidate.size
		remove = append(remove, candidate)
	}
	if (policy.MaxRuns > 0 && keptCount > policy.MaxRuns) ||
		(policy.MaxBytes > 0 && keptBytes > policy.MaxBytes) {
		result.Warnings = append(result.Warnings, "newest run was retained even though a retention bound is exceeded")
	}
	for _, candidate := range remove {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !dryRun {
			if err := os.RemoveAll(candidate.path); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("remove %s: %v", candidate.id, err))
				continue
			}
		}
		result.Removed = append(result.Removed, candidate.id)
		result.RemovedBytes += candidate.size
	}
	result.Kept = len(keep)
	result.KeptBytes = keptBytes
	return result, nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
