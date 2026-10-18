package cli

import (
	"debug/buildinfo"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/sbom"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// runSBOM emits a minimal SPDX SBOM from the binary's embedded build info.
func runSBOM(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mcp-native-diagnostics sbom [--output <file>] [--binary <path>] [--version <v>] [--name <artifact-name>] [--created <RFC3339>]")
	}
	output := flags.String("output", "", "write the SPDX JSON to this file instead of stdout")
	binary := flags.String("binary", "", "read build information from this binary instead of the running command")
	versionOverride := flags.String("version", "", "override the package version recorded in the SBOM")
	documentName := flags.String("name", "mcp-native-diagnostics", "document identity; release builds include the architecture")
	createdFlag := flags.String("created", "", "document creation time in RFC3339; defaults to now")
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
	var info *debug.BuildInfo
	if *binary != "" {
		embedded, err := buildinfo.ReadFile(*binary)
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: read build information from %q: %v\n", *binary, err)
			return ExitFailure
		}
		info = embedded
	} else {
		embedded, ok := debug.ReadBuildInfo()
		if !ok {
			fmt.Fprintln(stderr, "mcp-native-diagnostics: Go build information is unavailable")
			return ExitFailure
		}
		info = embedded
	}
	packageVersion := version.Current().Version
	if *binary != "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		packageVersion = info.Main.Version
	}
	if *versionOverride != "" {
		packageVersion = *versionOverride
	}
	created := time.Now().UTC()
	if *createdFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *createdFlag)
		if err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: invalid --created: %v\n", err)
			return ExitUsage
		}
		created = parsed
	}
	doc, err := sbom.Generate(info, *documentName, packageVersion, created)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: sbom: %v\n", err)
		return ExitFailure
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: sbom: %v\n", err)
		return ExitFailure
	}
	data = append(data, '\n')
	if *output == "" {
		if _, err := stdout.Write(data); err != nil {
			fmt.Fprintf(stderr, "mcp-native-diagnostics: sbom: %v\n", err)
			return ExitFailure
		}
		return ExitSuccess
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: write SBOM: %v\n", err)
		return ExitFailure
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		fmt.Fprintf(stderr, "mcp-native-diagnostics: write SBOM: %v\n", err)
		return ExitFailure
	}
	if err := file.Close(); err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: write SBOM: %v\n", err)
		return ExitFailure
	}
	return ExitSuccess
}
