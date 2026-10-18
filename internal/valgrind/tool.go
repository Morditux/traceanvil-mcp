package valgrind

import (
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

// Tool is one Valgrind tool normalized by this pipeline.
type Tool string

const (
	ToolMemcheck Tool = "memcheck"
	ToolHelgrind Tool = "helgrind"
	ToolDRD      Tool = "drd"
)

// Valid reports whether t is implemented.
func (t Tool) Valid() bool {
	switch t {
	case ToolMemcheck, ToolHelgrind, ToolDRD:
		return true
	default:
		return false
	}
}

// Origin returns the canonical diagnostic origin for t.
func (t Tool) Origin() string {
	switch t {
	case ToolHelgrind:
		return "valgrind-helgrind"
	case ToolDRD:
		return "valgrind-drd"
	default:
		return "valgrind-memcheck"
	}
}

// RawArtifactName returns the preserved XML file name for t.
func (t Tool) RawArtifactName() string {
	switch t {
	case ToolHelgrind:
		return "helgrind.xml"
	case ToolDRD:
		return "drd.xml"
	default:
		return RawArtifactName
	}
}

// ParserName returns the report parser/tool identifier used for compatibility.
func (t Tool) ParserName() string {
	return "valgrind-" + string(t)
}

// SeverityForKind applies the documented per-tool severity policy. It is a
// server classification, not a claim made by Valgrind. Unknown kinds are
// warnings and are never silently ignored.
func (t Tool) SeverityForKind(kind string) diagnostic.Severity {
	kind = strings.TrimSpace(kind)
	switch t {
	case ToolHelgrind:
		switch kind {
		case "Race", "LockOrder":
			return diagnostic.SeverityError
		case "UnlockUnlocked", "UnlockForeign", "UnlockBogus", "PthAPIerror", "Misc":
			return diagnostic.SeverityWarning
		default:
			return diagnostic.SeverityWarning
		}
	case ToolDRD:
		switch kind {
		case "ConflictingAccess":
			return diagnostic.SeverityError
		case "ThreadErr", "MutexErr", "CondErr", "BarrierErr", "RWLockErr", "Holdtime", "OtherErr":
			return diagnostic.SeverityWarning
		default:
			return diagnostic.SeverityWarning
		}
	default:
		return severityForKind(kind)
	}
}

// ToolFromString parses a tool name, defaulting empty to memcheck.
func ToolFromString(value string) (Tool, bool) {
	tool := Tool(strings.TrimSpace(value))
	if tool == "" {
		tool = ToolMemcheck
	}
	return tool, tool.Valid()
}
