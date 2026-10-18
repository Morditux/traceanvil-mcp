// Package policy resolves declared targets at execution time.
//
// Configuration validation is lexical and does not require targets to exist.
// This package revalidates existence, symlinks, executable permissions and
// allowed arguments immediately before a process is launched.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

// Resolved is a target that passed every runtime check.
type Resolved struct {
	TargetID string
	Program  string
	Dir      string
	Args     []string
}

// ReasonUnknownTarget is the machine-readable reason used when the target id
// is not declared in configuration.
const ReasonUnknownTarget = "unknown target"

// DeniedError reports a target that cannot be run under the current policy.
type DeniedError struct {
	TargetID string
	Reason   string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("policy: target %q denied: %s", e.TargetID, e.Reason)
}

// Resolver evaluates configured targets against the current filesystem.
type Resolver struct {
	roots   []string
	targets map[string]config.Target
}

// New builds a resolver from validated configuration. It performs no
// filesystem access, so a missing workspace root does not prevent the server
// from starting; Resolve denies affected targets instead.
func New(cfg *config.Config) *Resolver {
	resolver := &Resolver{targets: make(map[string]config.Target)}
	if cfg == nil {
		return resolver
	}

	resolver.roots = append([]string(nil), cfg.WorkspaceRoots...)
	for _, target := range cfg.AllowedTargets {
		target.AllowedArgs = append([]string(nil), target.AllowedArgs...)
		resolver.targets[target.ID] = target
	}
	return resolver
}

// Resolve validates one request and returns canonical paths.
//
// Known limitation: validation and execution are separate steps, so a program
// or directory that the server's user can replace inside a writable workspace
// may change between Resolve and exec. Callers must resolve immediately before
// launching; closing the window needs a system-level sandbox (AGENTS.md rule 9).
func (r *Resolver) Resolve(targetID string, args []string) (Resolved, error) {
	target, ok := r.targets[targetID]
	if !ok {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: ReasonUnknownTarget}
	}
	if err := validateArgs(target.AllowedArgs, args); err != nil {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: err.Error()}
	}

	roots := r.resolvedRoots()
	if len(roots) == 0 {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "no workspace root is available"}
	}
	if !filepath.IsAbs(target.Program) || !filepath.IsAbs(target.Cwd) {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program and cwd must be absolute paths"}
	}

	program, err := filepath.EvalSymlinks(target.Program)
	if err != nil {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program is not accessible"}
	}
	dir, err := filepath.EvalSymlinks(target.Cwd)
	if err != nil {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "working directory is not accessible"}
	}

	if !withinAny(roots, program) {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program resolves outside workspace roots"}
	}
	if !withinAny(roots, dir) {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "working directory resolves outside workspace roots"}
	}

	programInfo, err := os.Stat(program)
	if err != nil {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program is not accessible"}
	}
	if !programInfo.Mode().IsRegular() {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program is not a regular file"}
	}
	if programInfo.Mode().Perm()&0o111 == 0 {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "program is not executable"}
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "working directory is not accessible"}
	}
	if !dirInfo.IsDir() {
		return Resolved{}, &DeniedError{TargetID: targetID, Reason: "working directory is not a directory"}
	}

	return Resolved{
		TargetID: targetID,
		Program:  program,
		Dir:      dir,
		Args:     append([]string(nil), args...),
	}, nil
}

// Environment returns the minimal environment for a sandboxed target. It
// deliberately excludes os.Environ so server secrets are never inherited.
func Environment(tmpDir string) []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
	}
	if tmpDir != "" {
		env = append(env, "TMPDIR="+tmpDir)
	}
	return env
}

// HasUsableTarget reports whether at least one declared target currently
// resolves under policy. It performs filesystem checks only and never executes
// a target. Tool exposure uses this to hide a start tool that has no usable
// input on the current machine.
func (r *Resolver) HasUsableTarget() bool {
	for _, id := range r.TargetIDs() {
		if _, err := r.Resolve(id, nil); err == nil {
			return true
		}
	}
	return false
}

// TargetIDs returns the configured target identifiers in stable order.
func (r *Resolver) TargetIDs() []string {
	ids := make([]string, 0, len(r.targets))
	for id := range r.targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Resolver) resolvedRoots() []string {
	roots := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, resolved)
	}
	return roots
}

func withinAny(roots []string, path string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func validateArgs(allowed, requested []string) error {
	if len(requested) > config.MaxAllowedArgs {
		return fmt.Errorf("too many arguments: %d > %d", len(requested), config.MaxAllowedArgs)
	}

	allowedCounts := make(map[string]int, len(allowed))
	for _, arg := range allowed {
		allowedCounts[arg]++
	}

	requestedCounts := make(map[string]int, len(requested))
	for _, arg := range requested {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("argument contains NUL")
		}
		if len(arg) > config.MaxArgBytes {
			return fmt.Errorf("argument exceeds %d bytes", config.MaxArgBytes)
		}
		requestedCounts[arg]++
		if requestedCounts[arg] > allowedCounts[arg] {
			return fmt.Errorf("argument %q is not allowed", arg)
		}
	}
	return nil
}

// ResolvedRoots returns the workspace roots that currently exist and resolve,
// in configuration order. Analysis layers use them to normalize tool-reported
// source paths without inventing containment.
func (r *Resolver) ResolvedRoots() []string {
	return r.resolvedRoots()
}

// ResolvedSource is a source file that passed the workspace and regular-file
// checks. Path is absolute and canonical; Relative is slash-separated and
// relative to the matched workspace root.
type ResolvedSource struct {
	Path     string
	Relative string
	Root     string
}

// SourceError reports a source path rejected by the local policy.
type SourceError struct {
	Reason string
}

func (e *SourceError) Error() string {
	return "policy: source denied: " + e.Reason
}

// ResolveSource validates a source path immediately before a debugger uses it.
// A relative path is resolved against baseDir (the target working directory).
// Symlinks are resolved before containment is checked, so a symlink cannot
// escape a workspace root.
func (r *Resolver) ResolveSource(sourcePath, baseDir string) (ResolvedSource, error) {
	if sourcePath == "" {
		return ResolvedSource{}, &SourceError{Reason: "path must not be empty"}
	}
	if len(sourcePath) > config.MaxPathBytes {
		return ResolvedSource{}, &SourceError{Reason: "path is too long"}
	}
	if strings.ContainsRune(sourcePath, 0) {
		return ResolvedSource{}, &SourceError{Reason: "path contains NUL"}
	}
	if baseDir == "" || !filepath.IsAbs(baseDir) {
		return ResolvedSource{}, &SourceError{Reason: "base directory must be absolute"}
	}

	path := sourcePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ResolvedSource{}, &SourceError{Reason: "source file is not accessible"}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ResolvedSource{}, &SourceError{Reason: "source file is not accessible"}
	}
	if !info.Mode().IsRegular() {
		return ResolvedSource{}, &SourceError{Reason: "source is not a regular file"}
	}

	roots := r.resolvedRoots()
	rel, root, ok := relativeWithinAny(roots, resolved)
	if !ok {
		return ResolvedSource{}, &SourceError{Reason: "source resolves outside workspace roots"}
	}
	return ResolvedSource{
		Path:     resolved,
		Relative: filepath.ToSlash(rel),
		Root:     root,
	}, nil
}

// relativeWithinAny returns path relative to the first containing root.
func relativeWithinAny(roots []string, path string) (string, string, bool) {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			return rel, root, true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return rel, root, true
		}
	}
	return "", "", false
}
