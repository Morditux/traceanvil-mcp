// Command mcp-native-diagnostics is the entry point of the native dynamic
// analysis MCP server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Morditux/traceanvil-mcp/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
