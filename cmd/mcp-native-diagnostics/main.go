// Command mcp-native-diagnostics is the entry point of the native dynamic
// analysis MCP server. Repository scaffolding (J0) exposes the `version`
// command only; `serve`, `doctor` and `validate-config` arrive in J1.
package main

import (
	"os"

	"github.com/Morditux/traceanvil-mcp/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
