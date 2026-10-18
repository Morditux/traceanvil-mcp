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
// Discovery never runs a user program. Valgrind is queried with --version and
// --tool=memcheck --help to verify Memcheck XML support; GDB is queried with
// --version. LLDB discovers versioned lldb-dap candidates, treats --version as
// optional and verifies a DAP initialize handshake.
func NewConfigProbes(cfg *config.Config) []Probe {
	if cfg == nil {
		return nil
	}

	return []Probe{
		NewBinaryProbe(BackendValgrind, "valgrind", "valgrind", cfg.Backends.Valgrind,
			[]string{"memcheck", "xml-output", "suppressions"}).
			WithFunctionalCheck(
				[]string{"--tool=memcheck", "--help"},
				[]string{"memcheck", "--xml=yes", "--error-exitcode", "--suppressions"},
			).
			WithNote("Memcheck tool and XML options verified from help output; every run revalidates the binary"),
		NewGDBProbe(cfg.Backends.GDB).
			WithNote("no user program was run"),
		NewLLDBProbe(cfg.Backends.LLDB).
			WithNote("versioned adapter discovery; no user program was run"),
	}
}
