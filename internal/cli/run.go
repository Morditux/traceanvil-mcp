// Package cli implements the command-line entry point.
//
// It owns argument parsing and exit codes only. Analysis, policy and protocol
// logic live in their own packages and are wired in by later milestones. Every
// function here writes explicitly to the provided writers so that the MCP
// server can reserve stdout for the protocol when it is added in J1.
package cli

import (
	"fmt"
	"io"

	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// Exit codes. They are part of the CLI contract and documented in README.md.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const usage = `Usage:
  mcp-native-diagnostics <command> [arguments]

Commands:
  version    Print build information.
  help       Print this help.

Run "mcp-native-diagnostics help" for this message.
`

// Run dispatches a single command line and returns the process exit code.
// It never panics and never writes to a writer it was not given.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	switch args[0] {
	case "version":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: version takes no arguments\n\n")
			fmt.Fprint(stderr, usage)
			return ExitUsage
		}
		fmt.Fprintln(stdout, version.Current())
		return ExitSuccess

	case "help", "-h", "--help":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: help takes no arguments\n\n")
			fmt.Fprint(stderr, usage)
			return ExitUsage
		}
		fmt.Fprint(stdout, usage)
		return ExitSuccess

	default:
		fmt.Fprintf(stderr, "mcp-native-diagnostics: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
}
