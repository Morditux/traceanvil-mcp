package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
)

// runPurge applies the configured storage retention policy or a dry run. The
// server must be stopped: this command never coordinates with active jobs.
func runPurge(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("purge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mcp-native-diagnostics purge --config <file> [--dry-run]")
	}
	configPath := flags.String("config", "", "path to the JSON configuration file")
	dryRun := flags.Bool("dry-run", false, "report what would be removed without deleting it")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return ExitSuccess
		}
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: unexpected arguments: %v\n", flags.Args())
		return ExitUsage
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "mcp-native-diagnostics: --config is required")
		return ExitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	store, err := artifacts.NewStore(cfg.ReportDir, cfg.Limits.MaxReportBytes)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	if err := store.AcquireLock(); err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	defer store.ReleaseLock()
	result, err := store.Purge(ctx, artifacts.RetentionPolicy{
		MaxAge:  time.Duration(cfg.Storage.MaxAgeDays) * 24 * time.Hour,
		MaxRuns: cfg.Storage.MaxRuns, MaxBytes: cfg.Storage.MaxBytes,
	}, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: purge: %v\n", err)
		return ExitFailure
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: purge summary: %v\n", err)
		return ExitFailure
	}
	fmt.Fprintln(stdout, string(data))
	return ExitSuccess
}
