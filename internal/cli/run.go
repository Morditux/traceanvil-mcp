// Package cli implements the command-line entry point.
//
// It owns argument parsing, orchestration and exit codes only. Analysis,
// policy and protocol logic live in their own packages. Every function writes
// explicitly to the provided writers so that stdout can be reserved for the
// MCP protocol when serving.
package cli

import (
	"context"
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
  serve             Run the MCP server over stdio.
  doctor            Inspect configuration, paths and tool availability.
  validate-config   Validate a configuration file.
  version           Print build information.
  help              Print this help.

Use "mcp-native-diagnostics <command> --help" for command options.
`

// Run dispatches a single command line and returns the process exit code.
// It never panics and never writes to a writer it was not given.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}

	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stdin, stdout, stderr)

	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)

	case "validate-config":
		return runValidateConfig(args[1:], stdout, stderr)

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
