package capabilities

import (
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// Capability IDs for external backends.
const (
	BackendValgrind         = "backend.valgrind"
	BackendValgrindHelgrind = "backend.valgrind.helgrind"
	BackendValgrindDRD      = "backend.valgrind.drd"
	BackendGDB              = "backend.gdb"
	BackendLLDB             = "backend.lldb"
	BackendSanitizerASan    = "backend.sanitizer.asan"
	BackendSanitizerUBSan   = "backend.sanitizer.ubsan"
	BackendSanitizerLSan    = "backend.sanitizer.lsan"
	BackendSanitizerTSan    = "backend.sanitizer.tsan"
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
		NewBinaryProbe(BackendValgrindHelgrind, "valgrind", "valgrind", cfg.Backends.Valgrind,
			[]string{"helgrind", "xml-output", "suppressions"}).
			WithFunctionalCheck(
				[]string{"--tool=helgrind", "--help"},
				[]string{"user options for Helgrind", "--xml=yes", "--error-exitcode"},
			).
			WithNote("Helgrind XML and tool help verified; no user program was run"),
		NewBinaryProbe(BackendValgrindDRD, "valgrind", "valgrind", cfg.Backends.Valgrind,
			[]string{"drd", "xml-output", "suppressions"}).
			WithFunctionalCheck(
				[]string{"--tool=drd", "--help"},
				[]string{"user options for drd", "--xml=yes", "--error-exitcode"},
			).
			WithNote("DRD XML and tool help verified; no user program was run"),
		NewSanitizerProbe(cfg.Backends.Sanitizers, policy.New(cfg), "asan"),
		NewSanitizerProbe(cfg.Backends.Sanitizers, policy.New(cfg), "ubsan"),
		NewSanitizerProbe(cfg.Backends.Sanitizers, policy.New(cfg), "lsan"),
		NewSanitizerProbe(cfg.Backends.Sanitizers, policy.New(cfg), "tsan"),
		NewGDBProbe(cfg.Backends.GDB).
			WithNote("no user program was run"),
		NewLLDBProbe(cfg.Backends.LLDB).
			WithNote("versioned adapter discovery; no user program was run"),
	}
}
