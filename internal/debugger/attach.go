package debugger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AttachAvailable reports whether attach is both configured and allowlisted.
// It performs no process access.
func (s *Service) AttachAvailable() bool {
	return s != nil && s.attachEnabled && len(s.attachAllowed) > 0
}

// Attach starts a debugger session on one explicitly allowlisted process. The
// service revalidates ownership, the workspace confinement of /proc/<pid>/exe,
// ptrace policy (Yama) and process liveness immediately before the adapter is
// launched. It never changes a sysctl or uses sudo.
func (s *Service) Attach(ctx context.Context, req AttachRequest) (Status, error) {
	if s == nil || s.manager == nil {
		return Status{}, Errorf(CodeClosed, "debugger service is not configured")
	}
	if ctx == nil {
		return Status{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, Wrap(CodeCanceled, err, "attach was canceled")
	}
	if !s.attachEnabled {
		return Status{}, Errorf(CodeAttachDenied, "attach is disabled by policy")
	}
	if len(s.attachAllowed) == 0 {
		return Status{}, Errorf(CodeAttachDenied, "attach allowlist is empty")
	}
	if _, ok := s.attachAllowed[req.PID]; !ok {
		return Status{}, Errorf(CodeAttachDenied, "pid %d is not in the attach allowlist", req.PID)
	}
	backend, backendOptions, err := s.selectBackend(req.Backend)
	if err != nil {
		return Status{}, err
	}
	if backend != BackendGDB {
		return Status{}, Errorf(CodeUnsupported,
			"controlled attach is currently verified only for GDB; LLDB attach is not advertised")
	}
	program, cwd, identity, err := s.validateAttachProcess(req.PID)
	if err != nil {
		return Status{}, err
	}
	binary, version, err := backendOptions.BinaryProvider()
	if err != nil {
		return Status{}, Wrap(CodeBackendUnavailable, err, "%s backend is unavailable", backend)
	}
	if err := validateDebuggerBinary(binary); err != nil {
		return Status{}, Wrap(CodeBackendUnavailable, err, "%s backend is not usable", backend)
	}
	stopOnEntry := false
	if req.StopOnEntry != nil {
		stopOnEntry = *req.StopOnEntry
	}
	session, err := s.manager.Start(ctx, func(startCtx context.Context, id string) (Session, error) {
		return s.starter(startCtx, sessionOptions{
			ID:                   id,
			TargetID:             fmt.Sprintf("attach-%d", req.PID),
			Backend:              backend,
			Program:              program,
			Dir:                  cwd,
			Binary:               binary,
			Version:              version,
			StopOnEntry:          stopOnEntry,
			Roots:                append([]string(nil), s.roots...),
			Logger:               s.logger,
			RequestTimeout:       s.requestTimeout,
			StartupTimeout:       s.startupTimeout,
			StopTimeout:          s.stopTimeout,
			Attach:               true,
			PID:                  req.PID,
			AllowAttachTerminate: s.attachTerminate,
		})
	})
	if err != nil {
		var named *Error
		if errors.As(err, &named) {
			return Status{}, named
		}
		var limit *LimitError
		switch {
		case errors.As(err, &limit):
			return Status{}, Wrap(CodeQuotaExceeded, err, "session limit reached (%d live sessions)", limit.Max)
		case errors.Is(err, ErrClosed):
			return Status{}, Wrap(CodeClosed, err, "debugger service is closed")
		case errors.Is(err, context.Canceled):
			return Status{}, Wrap(CodeCanceled, err, "attach was canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return Status{}, Wrap(CodeTimeout, err, "attach timed out")
		default:
			return Status{}, err
		}
	}
	if !sameProcessIdentity(req.PID, identity) || !processTraced(req.PID) {
		session.Close()
		return Status{}, Errorf(CodeAttachDenied,
			"kernel did not accept attach to pid %d (Yama permission or process identity changed)", req.PID)
	}
	return session.Status(), nil
}

type processIdentity struct {
	startTime uint64
	dev       uint64
	inode     uint64
}

func (s *Service) validateAttachProcess(pid int) (string, string, processIdentity, error) {
	if pid <= 1 || pid == os.Getpid() {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d is not attachable", pid)
	}
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	status, err := os.ReadFile(filepath.Join(proc, "status"))
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "pid %d is not accessible", pid)
	}
	uid, state, tracer, err := parseProcStatus(string(status))
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "pid %d status is invalid", pid)
	}
	if uid != os.Geteuid() {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d is owned by another user", pid)
	}
	if state == "Z" || state == "X" {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d is not running", pid)
	}
	if tracer != 0 {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d is already traced", pid)
	}
	program, err := filepath.EvalSymlinks(filepath.Join(proc, "exe"))
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "pid %d executable is unavailable", pid)
	}
	cwd, err := filepath.EvalSymlinks(filepath.Join(proc, "cwd"))
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "pid %d working directory is unavailable", pid)
	}
	if len(s.roots) > 0 && (!withinRoot(s.roots, program) || !withinRoot(s.roots, cwd)) {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d resolves outside workspace roots", pid)
	}
	info, err := os.Stat(program)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "pid %d executable is not runnable", pid)
	}
	identity, err := readProcessIdentity(pid)
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "pid %d identity is unavailable", pid)
	}
	scope, err := yamaScope()
	if err != nil {
		return "", "", processIdentity{}, Wrap(CodeAttachDenied, err, "cannot determine Yama ptrace scope")
	}
	if scope >= 2 {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied, "Yama ptrace_scope=%d blocks attach; no sysctl change was attempted", scope)
	}
	if scope == 1 && !isDescendant(pid, os.Getpid()) {
		return "", "", processIdentity{}, Errorf(CodeAttachDenied,
			"Yama ptrace_scope=1 requires a descendant and a kernel-accepted tracer; the target may need PR_SET_PTRACER")
	}
	return program, cwd, identity, nil
}

func readProcessIdentity(pid int) (processIdentity, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return processIdentity{}, err
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 {
		return processIdentity{}, errors.New("invalid proc stat")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) <= 19 {
		return processIdentity{}, errors.New("proc stat has no starttime")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, err
	}
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return processIdentity{}, err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return processIdentity{}, errors.New("proc exe has no inode metadata")
	}
	return processIdentity{startTime: startTime, dev: uint64(sys.Dev), inode: sys.Ino}, nil
}

func sameProcessIdentity(pid int, before processIdentity) bool {
	after, err := readProcessIdentity(pid)
	return err == nil && after == before
}

func processTraced(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	_, state, tracer, err := parseProcStatus(string(data))
	if err != nil || tracer <= 0 {
		return false
	}
	parent, err := parentPID(tracer)
	if err != nil || parent != os.Getpid() {
		return false
	}
	return state == "T" || state == "t" || state == "R" || state == "S"
}

func parseProcStatus(data string) (int, string, int, error) {
	var uid int
	var state string
	var tracer int
	foundUID := false
	for _, line := range strings.Split(data, "\n") {
		switch {
		case strings.HasPrefix(line, "Uid:"):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, "", 0, errors.New("missing uid")
			}
			first := true
			for _, value := range fields[1:] {
				parsed, err := strconv.Atoi(value)
				if err != nil {
					return 0, "", 0, err
				}
				if first {
					uid = parsed
					first = false
				} else if parsed != uid {
					return 0, "", 0, errors.New("process real/effective/saved uids differ")
				}
			}
			foundUID = true
		case strings.HasPrefix(line, "State:"):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, "", 0, errors.New("missing state")
			}
			state = fields[1]
		case strings.HasPrefix(line, "TracerPid:"):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, "", 0, errors.New("missing tracer")
			}
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0, "", 0, err
			}
			tracer = value
		}
	}
	if !foundUID {
		return 0, "", 0, errors.New("missing uid")
	}
	return uid, state, tracer, nil
}

func yamaScope() (int, error) {
	data, err := os.ReadFile("/proc/sys/kernel/yama/ptrace_scope")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 1, nil
		}
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return value, nil
}

func isDescendant(pid, parent int) bool {
	seen := make(map[int]struct{}, 64)
	current := pid
	for depth := 0; depth < 64; depth++ {
		if current == parent {
			return true
		}
		if current <= 1 {
			return false
		}
		if _, duplicate := seen[current]; duplicate {
			return false
		}
		seen[current] = struct{}{}
		next, err := parentPID(current)
		if err != nil || next <= 0 || next == current {
			return false
		}
		current = next
	}
	return false
}

func attachedProcessAlive(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	_, state, _, err := parseProcStatus(string(data))
	return err == nil && state != "Z" && state != "X"
}

func parentPID(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 {
		return 0, errors.New("invalid proc stat")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("invalid proc stat fields")
	}
	return strconv.Atoi(fields[1])
}

func withinRoot(roots []string, path string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}
