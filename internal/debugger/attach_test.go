package debugger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

func TestAttachRequiresExplicitAllowlist(t *testing.T) {
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{t.TempDir()}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver,
		Backends: map[string]BackendOptions{
			BackendGDB: {Enabled: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if service.AttachAvailable() {
		t.Fatal("attach is available without an allowlist")
	}
	if _, err := service.Attach(context.Background(), AttachRequest{PID: 2}); CodeOf(err) != CodeAttachDenied {
		t.Fatalf("Attach() error = %v", err)
	}
}

func TestAttachChildAndDetach(t *testing.T) {
	gdb, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	if !filepath.IsAbs(gdb) {
		t.Skip("gdb resolved through a relative PATH entry")
	}
	root := t.TempDir()
	program := filepath.Join(root, "ptrace-target")
	source := filepath.Join(root, "ptrace-target.c")
	if err := os.WriteFile(source, []byte(
		"#include <sys/prctl.h>\n#include <unistd.h>\n"+
			"int main(void){ prctl(PR_SET_PTRACER, PR_SET_PTRACER_ANY); for(;;) sleep(1); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-o", program, source).CombinedOutput(); err != nil {
		t.Skipf("compile ptrace target: %v\n%s", err, output)
	}
	child := exec.Command(program)
	child.Dir = root
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		_ = child.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{root}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver,
		Roots:    resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{
			BackendGDB: {
				Enabled:        true,
				BinaryProvider: func() (string, string, error) { return gdb, "gdb", nil },
				Available:      func() (bool, string) { return true, "" },
			},
		},
		AttachEnabled:     true,
		AttachAllowedPIDs: []int{child.Process.Pid},
		MaxSessions:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if !service.AttachAvailable() {
		t.Fatal("attach is not available with an explicit allowlist")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := service.Attach(ctx, AttachRequest{PID: child.Process.Pid})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if !status.Attached || status.SessionID == "" {
		t.Fatalf("attach status = %+v", status)
	}
	procStatus, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(procStatus), "TracerPid:\t") ||
		strings.Contains(string(procStatus), "TracerPid:\t0") {
		t.Fatal("attach did not establish a kernel tracer")
	}
	kill := true
	if _, err := service.Stop(ctx, StopRequest{SessionID: status.SessionID, Terminate: &kill}); CodeOf(err) != CodeUnsupported {
		t.Fatalf("terminate attached without policy permission = %v", err)
	}
	stop, err := service.Stop(ctx, StopRequest{SessionID: status.SessionID})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !stop.Detached || stop.Terminated {
		t.Fatalf("stop result = %+v", stop)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("attached child did not survive detach: %v", err)
	}
}

func TestAttachWithoutPtracerIsDenied(t *testing.T) {
	gdb, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	root := t.TempDir()
	program := filepath.Join(root, "sleep")
	data, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skipf("read /bin/sleep: %v", err)
	}
	if err := os.WriteFile(program, data, 0o700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(program, "30")
	child.Dir = root
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		_ = child.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{root}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver, Roots: resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{BackendGDB: {
			Enabled:        true,
			BinaryProvider: func() (string, string, error) { return gdb, "gdb", nil },
			Available:      func() (bool, string) { return true, "" },
		}},
		AttachEnabled: true, AttachAllowedPIDs: []int{child.Process.Pid}, MaxSessions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := service.Attach(ctx, AttachRequest{PID: child.Process.Pid}); CodeOf(err) != CodeAttachDenied {
		t.Fatalf("attach without ptracer error = %v", err)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("denied attach killed the target: %v", err)
	}
}

func TestAttachedChildSurvivesServiceClose(t *testing.T) {
	gdb, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	root := t.TempDir()
	program := filepath.Join(root, "ptrace-target")
	source := filepath.Join(root, "ptrace-target.c")
	if err := os.WriteFile(source, []byte(
		"#include <sys/prctl.h>\n#include <unistd.h>\n"+
			"int main(void){ prctl(PR_SET_PTRACER, PR_SET_PTRACER_ANY); for(;;) sleep(1); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-o", program, source).CombinedOutput(); err != nil {
		t.Skipf("compile ptrace target: %v\n%s", err, output)
	}
	child := exec.Command(program)
	child.Dir = root
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		_ = child.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{root}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver, Roots: resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{BackendGDB: {
			Enabled:        true,
			BinaryProvider: func() (string, string, error) { return gdb, "gdb", nil },
			Available:      func() (bool, string) { return true, "" },
		}},
		AttachEnabled: true, AttachAllowedPIDs: []int{child.Process.Pid}, MaxSessions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := service.Attach(ctx, AttachRequest{PID: child.Process.Pid}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	service.Close()
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("attached child did not survive service close: %v", err)
	}
}

func TestAttachTerminateWithPermission(t *testing.T) {
	gdb, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	root := t.TempDir()
	program := filepath.Join(root, "ptrace-target")
	source := filepath.Join(root, "ptrace-target.c")
	if err := os.WriteFile(source, []byte(
		"#include <sys/prctl.h>\n#include <unistd.h>\n"+
			"int main(void){ prctl(PR_SET_PTRACER, PR_SET_PTRACER_ANY); for(;;) sleep(1); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-o", program, source).CombinedOutput(); err != nil {
		t.Skipf("compile ptrace target: %v\n%s", err, output)
	}
	child := exec.Command(program)
	child.Dir = root
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		_ = child.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{root}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver, Roots: resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{BackendGDB: {
			Enabled:        true,
			BinaryProvider: func() (string, string, error) { return gdb, "gdb", nil },
			Available:      func() (bool, string) { return true, "" },
		}},
		AttachEnabled: true, AttachAllowedPIDs: []int{child.Process.Pid},
		AttachTerminate: true, MaxSessions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := service.Attach(ctx, AttachRequest{PID: child.Process.Pid})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	terminate := true
	stop, err := service.Stop(ctx, StopRequest{SessionID: status.SessionID, Terminate: &terminate})
	if err != nil {
		t.Fatalf("Stop(terminate) error = %v", err)
	}
	if !stop.Terminated {
		t.Fatalf("stop result = %+v", stop)
	}
	if attachedProcessAlive(child.Process.Pid) {
		t.Error("attached child survived an explicitly permitted terminate")
	}
}

func TestAttachRejectsUnsafePIDsDespiteAllowlist(t *testing.T) {
	gdb, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{t.TempDir()}})
	service, err := NewService(ServiceOptions{
		Resolver: resolver, Roots: resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{BackendGDB: {
			Enabled:        true,
			BinaryProvider: func() (string, string, error) { return gdb, "gdb", nil },
			Available:      func() (bool, string) { return true, "" },
		}},
		AttachEnabled: true, AttachAllowedPIDs: []int{1, os.Getpid()}, MaxSessions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, pid := range []int{1, os.Getpid(), 99999999} {
		if _, err := service.Attach(context.Background(), AttachRequest{PID: pid}); CodeOf(err) != CodeAttachDenied {
			t.Errorf("Attach(%d) error = %v", pid, err)
		}
	}
}
