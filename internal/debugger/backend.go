package debugger

import "fmt"

// backendSpec is the fixed, non-client-controlled configuration of one DAP
// debugger backend.
type backendSpec struct {
	Name      string
	AdapterID string
	Args      []string
	// InitializedAfterLaunch records the adapter's DAP ordering. GDB sends the
	// initialized event before launch; lldb-dap sends it after launch.
	InitializedAfterLaunch bool
	// DefaultThreadFromProcess allows a backend whose thread ids are process
	// ids (lldb-dap) to seed a pause target from the process event.
	DefaultThreadFromProcess bool
	// HardeningCheck rejects startup when the adapter reported a rejected
	// hardening command. Nil means the backend only uses fixed valid settings.
	HardeningCheck func(stderr string) error
}

func backendSpecFor(name string) (backendSpec, error) {
	switch name {
	case BackendGDB:
		return backendSpec{
			Name:           BackendGDB,
			AdapterID:      "gdb",
			Args:           gdbCommandArgs(),
			HardeningCheck: ValidateGDBHardening,
		}, nil
	case BackendLLDB:
		return backendSpec{
			Name:                     BackendLLDB,
			AdapterID:                "lldb",
			Args:                     lldbCommandArgs(),
			InitializedAfterLaunch:   true,
			DefaultThreadFromProcess: true,
		}, nil
	default:
		return backendSpec{}, fmt.Errorf("unknown debugger backend %q", name)
	}
}
