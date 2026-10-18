package capabilities

import "github.com/Morditux/traceanvil-mcp/internal/config"

// Capability IDs for external backends.
const (
	BackendValgrind = "backend.valgrind"
	BackendGDB      = "backend.gdb"
	BackendLLDB     = "backend.lldb"
)

// NewConfigProbes builds the discovery probes implied by configuration.
//
// Discovery never runs a user program. Valgrind and GDB are queried with
// --version; lldb-dap is only resolved because its --version behaviour is not
// guaranteed to terminate.
func NewConfigProbes(cfg *config.Config) []Probe {
	if cfg == nil {
		return nil
	}

	return []Probe{
		NewBinaryProbe(BackendValgrind, "valgrind", "valgrind", cfg.Backends.Valgrind, nil).
			WithNote("functional probe planned for J3"),
		NewBinaryProbe(BackendGDB, "gdb", "gdb", cfg.Backends.GDB, nil).
			WithNote("DAP probe planned for J6"),
		NewBinaryProbe(BackendLLDB, "lldb", "lldb-dap", cfg.Backends.LLDB, nil).
			WithoutVersionProbe().
			WithNote("DAP handler probe planned for J7"),
	}
}
