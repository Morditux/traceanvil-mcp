package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// runDoctor reports configuration, path and capability state without executing
// any user program or modifying the machine.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	configPath, exit := parseConfigFlags("doctor", args, stderr)
	if exit >= 0 {
		return exit
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}

	registry := capabilities.NewRegistry(capabilities.NewConfigProbes(cfg), 0)
	registry.Refresh(ctx)

	server := mcpserver.New(cfg, registry, version.Current(), nil)
	printReport(stdout, configPath, server.BuildReport(), filesystemWarnings(cfg))
	return ExitSuccess
}

// filesystemWarnings inspects configured paths with stat only. Missing tools or
// unbuilt targets are warnings, not startup failures: absence of a backend must
// not prevent the server from running.
func filesystemWarnings(cfg *config.Config) []string {
	var warnings []string

	for _, root := range cfg.WorkspaceRoots {
		info, err := os.Stat(root)
		switch {
		case errors.Is(err, os.ErrNotExist):
			warnings = append(warnings, fmt.Sprintf("workspace root %q does not exist", root))
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("workspace root %q is not accessible: %v", root, err))
		case !info.IsDir():
			warnings = append(warnings, fmt.Sprintf("workspace root %q is not a directory", root))
		}
	}

	for _, target := range cfg.AllowedTargets {
		info, err := os.Stat(target.Program)
		switch {
		case errors.Is(err, os.ErrNotExist):
			warnings = append(warnings,
				fmt.Sprintf("target %q program %q does not exist (it may need to be built)", target.ID, target.Program))
		case err != nil:
			warnings = append(warnings,
				fmt.Sprintf("target %q program %q is not accessible: %v", target.ID, target.Program, err))
		case info.IsDir():
			warnings = append(warnings,
				fmt.Sprintf("target %q program %q is a directory", target.ID, target.Program))
		case info.Mode().Perm()&0o111 == 0:
			warnings = append(warnings,
				fmt.Sprintf("target %q program %q is not executable", target.ID, target.Program))
		}

		info, err = os.Stat(target.Cwd)
		switch {
		case errors.Is(err, os.ErrNotExist):
			warnings = append(warnings, fmt.Sprintf("target %q cwd %q does not exist", target.ID, target.Cwd))
		case err != nil:
			warnings = append(warnings,
				fmt.Sprintf("target %q cwd %q is not accessible: %v", target.ID, target.Cwd, err))
		case !info.IsDir():
			warnings = append(warnings,
				fmt.Sprintf("target %q cwd %q is not a directory", target.ID, target.Cwd))
		}
	}

	info, err := os.Stat(cfg.ReportDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		warnings = append(warnings,
			fmt.Sprintf("report directory %q does not exist yet; analysis storage will create it", cfg.ReportDir))
	case err != nil:
		warnings = append(warnings,
			fmt.Sprintf("report directory %q is not accessible: %v", cfg.ReportDir, err))
	case !info.IsDir():
		warnings = append(warnings,
			fmt.Sprintf("report directory %q is not a directory", cfg.ReportDir))
	}

	return warnings
}

func printReport(stdout io.Writer, configPath string, report mcpserver.Report, extraWarnings []string) {
	fmt.Fprintf(stdout, "config: %s (valid)\n", configPath)
	fmt.Fprintf(stdout, "server: %s %s (commit %s, date %s, %s)\n",
		report.Server.Name,
		report.Server.Version,
		report.Server.Commit,
		report.Server.Date,
		report.Server.GoVersion,
	)
	if report.Server.Modified {
		fmt.Fprintln(stdout, "server: built from a modified checkout")
	}
	fmt.Fprintf(stdout, "profile: %s (attach=%t, evaluate=%t)\n",
		report.Policy.Profile, report.Policy.Attach, report.Policy.Evaluate)
	fmt.Fprintf(stdout, "limits: timeout=%ds jobs=%d sessions=%d report_bytes=%d response_items=%d\n",
		report.Limits.TimeoutSeconds,
		report.Limits.MaxJobs,
		report.Limits.MaxSessions,
		report.Limits.MaxReportBytes,
		report.Limits.MaxResponseItems,
	)

	fmt.Fprintln(stdout, "capabilities:")
	for _, capability := range report.Capabilities {
		line := fmt.Sprintf("  %-20s %-12s", capability.ID, capability.State)
		if capability.Version != "" {
			line += " version=" + capability.Version
		}
		if capability.Path != "" {
			line += " path=" + capability.Path
		}
		if capability.Reason != "" {
			line += " (" + capability.Reason + ")"
		}
		fmt.Fprintln(stdout, line)
	}

	tools := append([]mcpserver.ToolStatus(nil), report.Tools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	fmt.Fprintln(stdout, "tools:")
	for _, tool := range tools {
		exposed := "hidden"
		if tool.Exposed {
			exposed = "exposed"
		}
		fmt.Fprintf(stdout, "  %-24s %-12s %s\n", tool.Name, exposed, tool.Reason)
	}

	warnings := append(append([]string(nil), report.Warnings...), extraWarnings...)
	if len(warnings) > 0 {
		fmt.Fprintln(stdout, "warnings:")
		for _, warning := range warnings {
			fmt.Fprintf(stdout, "  - %s\n", warning)
		}
	}
}
