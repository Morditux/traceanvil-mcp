package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// capabilityRefreshInterval controls independent revalidation of backend
// capabilities. Tool exposure changes trigger tools/list_changed; no user
// program is ever executed by a probe.
const capabilityRefreshInterval = 30 * time.Second

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

	// Cleanup runs in reverse creation order: analysis cache, Memcheck service
	// (cancels and waits for active analyses), then the job manager.
	var cleanups []func()
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	store, err := artifacts.NewStore(cfg.ReportDir, cfg.Limits.MaxReportBytes)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}

	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{
		MaxJobs: cfg.Limits.MaxJobs,
		Timeout: time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	cleanups = append(cleanups, manager.Close)

	resolver := policy.New(cfg)
	valgrindService, err := valgrind.NewService(valgrind.ServiceOptions{
		BinaryProvider: func() (string, string, error) {
			record, ok := registry.Get(capabilities.BackendValgrind)
			if !ok {
				return "", "", &valgrind.BackendError{Reason: "valgrind capability is not registered"}
			}
			if record.State != capabilities.StateAvailable || record.Path == "" {
				reason := record.Reason
				if reason == "" {
					reason = "valgrind is " + string(record.State)
				}
				return "", "", &valgrind.BackendError{Reason: reason}
			}
			return record.Path, record.Version, nil
		},
		Store:        store,
		Manager:      manager,
		Resolver:     resolver,
		Roots:        resolver.ResolvedRoots(),
		Suppressions: cfg.Valgrind.Suppressions,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	cleanups = append(cleanups, valgrindService.Close)

	maxReportBytes := store.MaxBytesPerRun()
	if maxReportBytes > analysis.DefaultMaxReportBytes {
		maxReportBytes = analysis.DefaultMaxReportBytes
	}
	debugService, err := newDebuggerService(cfg, resolver, registry, logger)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	if debugService != nil {
		cleanups = append(cleanups, debugService.Close)
	}

	analysisService, err := analysis.New(analysis.Options{
		Store:          store,
		Backend:        analysis.NewValgrindBackend(valgrindService),
		BackendEnabled: cfg.Backends.Valgrind,
		BackendAvailable: func() (bool, string) {
			record, ok := registry.Get(capabilities.BackendValgrind)
			if !ok {
				return false, "valgrind capability is not registered"
			}
			return record.State == capabilities.StateAvailable, record.Reason
		},
		Timeout:          time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
		MaxResponseItems: cfg.Limits.MaxResponseItems,
		MaxReportBytes:   maxReportBytes,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	cleanups = append(cleanups, analysisService.Close)

	server := mcpserver.New(cfg, registry, version.Current(), logger)
	for _, definition := range mcpserver.AnalysisDefinitions(analysisService) {
		if err := server.Register(definition); err != nil {
			logger.Error("register analysis tool", "tool", definition.Name, "error", err)
			return ExitFailure
		}
	}
	if debugService != nil {
		for _, definition := range mcpserver.DebugDefinitions(debugService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register debug tool", "tool", definition.Name, "error", err)
				return ExitFailure
			}
		}
	}
	server.SyncTools()

	refreshCtx, stopRefresh := context.WithCancel(ctx)
	var refreshWG sync.WaitGroup
	refreshWG.Add(1)
	go func() {
		defer refreshWG.Done()
		runCapabilityRefresh(refreshCtx, capabilityRefreshInterval, func(ctx context.Context) (added, removed []string) {
			registry.Refresh(ctx)
			if ctx.Err() != nil {
				return nil, nil
			}
			return server.SyncTools()
		}, logger)
	}()

	logger.Info("serving MCP over stdio",
		"profile", cfg.Policy.Profile,
		"targets", len(cfg.AllowedTargets),
	)

	transport := &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopWriteCloser{Writer: stdout},
	}
	err = server.Run(ctx, transport)

	stopRefresh()
	refreshWG.Wait()

	// A context-driven shutdown and an EOF on stdin are normal MCP stdio
	// terminations, not failures.
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		logger.Error("MCP server stopped with an error", "error", err)
		return ExitFailure
	}
	return ExitSuccess
}

// newDebuggerService builds the GDB debug API when the backend is enabled.
// A disabled backend gets no debug tools at all; an enabled but currently
// unavailable backend keeps management tools hidden until it can be verified.
func newDebuggerService(cfg *config.Config, resolver *policy.Resolver, registry *capabilities.Registry, logger *slog.Logger) (*debugger.Service, error) {
	if !cfg.Backends.GDB {
		return nil, nil
	}
	return debugger.NewService(debugger.ServiceOptions{
		Resolver:       resolver,
		Roots:          resolver.ResolvedRoots(),
		BackendEnabled: true,
		BinaryProvider: func() (string, string, error) {
			record, ok := registry.Get(capabilities.BackendGDB)
			if !ok {
				return "", "", errors.New("backend.gdb capability is not registered")
			}
			if record.State != capabilities.StateAvailable || record.Path == "" {
				reason := record.Reason
				if reason == "" {
					reason = "gdb is " + string(record.State)
				}
				return "", "", errors.New(reason)
			}
			return record.Path, record.Version, nil
		},
		BackendAvailable: func() (bool, string) {
			record, ok := registry.Get(capabilities.BackendGDB)
			if !ok {
				return false, "backend.gdb capability is not registered"
			}
			return record.State == capabilities.StateAvailable, record.Reason
		},
		Logger:           logger,
		MaxSessions:      cfg.Limits.MaxSessions,
		MaxResponseItems: cfg.Limits.MaxResponseItems,
	})
}

// refreshFunc revalidates capabilities and reconciles tool exposure.
type refreshFunc func(context.Context) (added, removed []string)

// runCapabilityRefresh revalidates capabilities on a fixed interval,
// independently of business calls. It returns when ctx is canceled.
func runCapabilityRefresh(ctx context.Context, interval time.Duration, refresh refreshFunc, logger *slog.Logger) {
	if interval <= 0 {
		interval = capabilityRefreshInterval
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			added, removed := refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			if len(added) > 0 || len(removed) > 0 {
				logger.Info("capability refresh changed the tool list",
					"added", added, "removed", removed)
			}
		}
	}
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
