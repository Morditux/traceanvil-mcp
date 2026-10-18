package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	configPath, exit := parseConfigFlags("serve", args, stderr)
	if exit >= 0 {
		return exit
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	registry := capabilities.NewRegistry(capabilities.NewConfigProbes(cfg), 0)
	registry.Refresh(ctx)

	server := mcpserver.New(cfg, registry, version.Current(), logger)
	logger.Info("serving MCP over stdio",
		"profile", cfg.Policy.Profile,
		"targets", len(cfg.AllowedTargets),
	)

	transport := &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopWriteCloser{Writer: stdout},
	}
	err = server.Run(ctx, transport)
	// A context-driven shutdown and an EOF on stdin are normal MCP stdio
	// terminations, not failures.
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		logger.Error("MCP server stopped with an error", "error", err)
		return ExitFailure
	}
	return ExitSuccess
}

// parseConfigFlags parses "command --config <file>". It returns a non-negative
// exit code when the command must stop, and -1 when parsing succeeded.
func parseConfigFlags(command string, args []string, stderr io.Writer) (string, int) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: mcp-native-diagnostics %s --config <file>\n", command)
	}

	configPath := flags.String("config", "", "path to the JSON configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", ExitSuccess
		}
		return "", ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: unexpected arguments: %v\n", flags.Args())
		flags.Usage()
		return "", ExitUsage
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "mcp-native-diagnostics: --config is required")
		flags.Usage()
		return "", ExitUsage
	}
	return *configPath, -1
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
