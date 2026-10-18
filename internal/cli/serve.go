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
	"github.com/Morditux/traceanvil-mcp/internal/core"
	"github.com/Morditux/traceanvil-mcp/internal/ctest"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
	"github.com/Morditux/traceanvil-mcp/internal/perf"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/profile"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/sanitizer"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
	"github.com/Morditux/traceanvil-mcp/internal/version"
	"github.com/Morditux/traceanvil-mcp/internal/vgdb"
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

	if err := store.AcquireLock(); err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}
	cleanups = append(cleanups, store.ReleaseLock)

	if cfg.Storage.PurgeOnStart {
		result, purgeErr := store.Purge(ctx, artifacts.RetentionPolicy{
			MaxAge:  time.Duration(cfg.Storage.MaxAgeDays) * 24 * time.Hour,
			MaxRuns: cfg.Storage.MaxRuns, MaxBytes: cfg.Storage.MaxBytes,
		}, false)
		if purgeErr != nil {
			logger.Warn("startup artifact purge failed", "error", purgeErr)
		} else {
			logger.Info("startup artifact purge completed",
				"removed", len(result.Removed), "kept", result.Kept,
				"removed_bytes", result.RemovedBytes, "kept_bytes", result.KeptBytes)
		}
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

	var profileService *profile.Service
	if cfg.Backends.Valgrind {
		profileService, err = profile.NewService(profile.ServiceOptions{
			Store: store, Manager: manager, Resolver: resolver,
			Roots: resolver.ResolvedRoots(),
			BinaryProvider: func() (string, string, error) {
				record, ok := registry.Get(capabilities.BackendValgrind)
				if !ok {
					return "", "", errors.New("valgrind capability is not registered")
				}
				if record.State != capabilities.StateAvailable || record.Path == "" {
					reason := record.Reason
					if reason == "" {
						reason = "valgrind is " + string(record.State)
					}
					return "", "", errors.New(reason)
				}
				return record.Path, record.Version, nil
			},
			ToolAvailable: func(tool profile.Tool) (bool, string) {
				id := capabilities.BackendValgrindCallgrind
				if tool == profile.ToolMassif {
					id = capabilities.BackendValgrindMassif
				}
				record, ok := registry.Get(id)
				if !ok {
					return false, id + " capability is not registered"
				}
				if record.State != capabilities.StateAvailable {
					return false, record.Reason
				}
				return true, ""
			},
			MaxTimeout: time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
	}

	var perfService *perf.Service
	if cfg.Backends.Perf {
		perfService, err = perf.NewService(perf.ServiceOptions{
			Store: store, Manager: manager, Resolver: resolver,
			BinaryProvider: func() (string, string, error) {
				record, ok := registry.Get(capabilities.BackendPerf)
				if !ok || record.State != capabilities.StateAvailable || record.Path == "" {
					reason := "backend.perf is unavailable"
					if ok && record.Reason != "" {
						reason = record.Reason
					}
					return "", "", errors.New(reason)
				}
				return record.Path, record.Version, nil
			},
			MaxTimeout: time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
	}

	var vgdbService *vgdb.Service
	if cfg.Backends.Vgdb {
		valgrindProvider := func() (string, string, error) {
			record, ok := registry.Get(capabilities.BackendValgrind)
			if !ok || record.State != capabilities.StateAvailable || record.Path == "" {
				reason := "backend.valgrind is unavailable"
				if ok && record.Reason != "" {
					reason = record.Reason
				}
				return "", "", errors.New(reason)
			}
			return record.Path, record.Version, nil
		}
		vgdbProvider := func() (string, string, error) {
			record, ok := registry.Get(capabilities.BackendVgdb)
			if !ok || record.State != capabilities.StateAvailable || record.Path == "" {
				reason := "backend.vgdb is unavailable"
				if ok && record.Reason != "" {
					reason = record.Reason
				}
				return "", "", errors.New(reason)
			}
			return record.Path, record.Version, nil
		}
		vgdbService, err = vgdb.NewService(vgdb.ServiceOptions{
			Store: store, Manager: manager, Resolver: resolver,
			ValgrindProvider: valgrindProvider, VgdbProvider: vgdbProvider,
			MaxTimeout: time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
	}

	backends := map[string]analysis.Backend{
		analysis.ToolMemcheck: analysis.NewValgrindBackend(valgrindService),
		analysis.ToolHelgrind: analysis.NewValgrindBackend(valgrindService),
		analysis.ToolDRD:      analysis.NewValgrindBackend(valgrindService),
	}
	if cfg.Backends.Sanitizers {
		sanitizerService, err := sanitizer.NewService(sanitizer.ServiceOptions{
			Store: store, Manager: manager, Resolver: resolver,
			Roots: resolver.ResolvedRoots(),
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
		cleanups = append(cleanups, sanitizerService.Close)
		sanitizerBackend := analysis.NewSanitizerBackend(sanitizerService)
		for _, tool := range []string{analysis.ToolASan, analysis.ToolUBSan, analysis.ToolLSan, analysis.ToolTSan} {
			backends[tool] = sanitizerBackend
		}
	}

	var ctestService *ctest.Service
	if cfg.Backends.CTest {
		ctestService, err = ctest.NewService(ctest.ServiceOptions{
			Store:   store,
			Manager: manager,
			Runner:  runner.New(),
			Binary: func() (string, string, error) {
				record, ok := registry.Get(capabilities.BackendCTest)
				if !ok {
					return "", "", errors.New("ctest capability is not registered")
				}
				if record.State != capabilities.StateAvailable || record.Path == "" {
					reason := record.Reason
					if reason == "" {
						reason = "ctest is " + string(record.State)
					}
					return "", "", errors.New(reason)
				}
				return record.Path, record.Version, nil
			},
			Roots:              resolver.ResolvedRoots(),
			Projects:           cfg.CTest.Projects,
			DefaultTestTimeout: time.Duration(cfg.CTest.TestTimeoutSeconds) * time.Second,
			MaxTimeout:         time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
			MaxReportBytes:     store.MaxBytesPerRun(),
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
		cleanups = append(cleanups, ctestService.Close)
	}

	var coreService *core.Service
	if len(cfg.Cores) > 0 {
		coreService, err = core.NewService(core.ServiceOptions{
			Runner:   runner.New(),
			Profiles: cfg.Cores,
			Roots:    resolver.ResolvedRoots(),
			Timeout:  time.Duration(cfg.Limits.TimeoutSeconds) * time.Second,
			Store:    store,
			Binary: func() (string, string, error) {
				record, ok := registry.Get(capabilities.BackendGDB)
				if !ok {
					return "", "", errors.New("gdb capability is not registered")
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
		})
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
			return ExitFailure
		}
	}

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
		Backends:       backends,
		BackendEnabled: cfg.Backends.Valgrind || cfg.Backends.Sanitizers,
		BackendAvailable: func() (bool, string) {
			record, ok := registry.Get(capabilities.BackendValgrind)
			if !ok {
				return false, "valgrind capability is not registered"
			}
			return record.State == capabilities.StateAvailable, record.Reason
		},
		BackendAvailableFor: func(tool string) (bool, string) {
			id := capabilities.BackendValgrind
			switch tool {
			case analysis.ToolHelgrind:
				id = capabilities.BackendValgrindHelgrind
			case analysis.ToolDRD:
				id = capabilities.BackendValgrindDRD
			case analysis.ToolASan:
				id = capabilities.BackendSanitizerASan
			case analysis.ToolUBSan:
				id = capabilities.BackendSanitizerUBSan
			case analysis.ToolLSan:
				id = capabilities.BackendSanitizerLSan
			case analysis.ToolTSan:
				id = capabilities.BackendSanitizerTSan
			}
			record, ok := registry.Get(id)
			if !ok {
				return false, id + " capability is not registered"
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
	if ctestService != nil {
		for _, definition := range mcpserver.CTestDefinitions(ctestService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register ctest tool", "tool", definition.Name, "error", err)
				return ExitFailure
			}
		}
	}
	if coreService != nil {
		for _, definition := range mcpserver.CoreDefinitions(coreService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register core tool", "tool", definition.Name, "error", err)
				return ExitFailure
			}
		}
	}
	if profileService != nil {
		for _, definition := range mcpserver.ProfileDefinitions(profileService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register profile tool", "tool", definition.Name, "error", err)
				return ExitFailure
			}
		}
	}
	if perfService != nil {
		for _, definition := range mcpserver.PerfDefinitions(perfService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register perf tool", "tool", definition.Name, "error", err)
				return ExitFailure
			}
		}
	}
	if vgdbService != nil {
		for _, definition := range mcpserver.VgdbDefinitions(vgdbService, registry) {
			if err := server.Register(definition); err != nil {
				logger.Error("register vgdb tool", "tool", definition.Name, "error", err)
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

// newDebuggerService builds the debug API when at least one debugger backend is
// enabled. Disabled entries remain in the map so an explicit backend selection
// returns backend_disabled rather than an unknown-backend error.
func newDebuggerService(cfg *config.Config, resolver *policy.Resolver, registry *capabilities.Registry, logger *slog.Logger) (*debugger.Service, error) {
	if !cfg.Backends.GDB && !cfg.Backends.LLDB {
		return nil, nil
	}
	return debugger.NewService(debugger.ServiceOptions{
		Resolver: resolver,
		Roots:    resolver.ResolvedRoots(),
		Backends: map[string]debugger.BackendOptions{
			debugger.BackendGDB:  debugBackendOptions(registry, capabilities.BackendGDB, cfg.Backends.GDB),
			debugger.BackendLLDB: debugBackendOptions(registry, capabilities.BackendLLDB, cfg.Backends.LLDB),
		},
		Logger:            logger,
		MaxSessions:       cfg.Limits.MaxSessions,
		MaxResponseItems:  cfg.Limits.MaxResponseItems,
		AttachEnabled:     cfg.Policy.Attach,
		AttachAllowedPIDs: cfg.Policy.AttachAllowedPIDs,
		AttachTerminate:   cfg.Policy.AttachTerminate,
	})
}

func debugBackendOptions(registry *capabilities.Registry, id string, enabled bool) debugger.BackendOptions {
	options := debugger.BackendOptions{Enabled: enabled}
	if !enabled {
		return options
	}
	options.BinaryProvider = func() (string, string, error) {
		record, ok := registry.Get(id)
		if !ok {
			return "", "", fmt.Errorf("%s capability is not registered", id)
		}
		if record.State != capabilities.StateAvailable || record.Path == "" {
			reason := record.Reason
			if reason == "" {
				reason = id + " is " + string(record.State)
			}
			return "", "", errors.New(reason)
		}
		return record.Path, record.Version, nil
	}
	options.Available = func() (bool, string) {
		record, ok := registry.Get(id)
		if !ok {
			return false, fmt.Sprintf("%s capability is not registered", id)
		}
		return record.State == capabilities.StateAvailable, record.Reason
	}
	return options
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
