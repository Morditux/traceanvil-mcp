package cli

import (
	"fmt"
	"io"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func runValidateConfig(args []string, stdout, stderr io.Writer) int {
	configPath, exit := parseConfigFlags("validate-config", args, stderr)
	if exit >= 0 {
		return exit
	}

	if _, err := config.Load(configPath); err != nil {
		fmt.Fprintf(stderr, "mcp-native-diagnostics: %v\n", err)
		return ExitFailure
	}

	fmt.Fprintf(stdout, "config valid: %s\n", configPath)
	return ExitSuccess
}
