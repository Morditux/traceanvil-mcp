package ctest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

type ctestJSON struct {
	Tests []struct {
		Name string `json:"name"`
	} `json:"tests"`
}

func parseDiscovery(data []byte, project config.CTestProject) (Discovery, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Discovery{}, fmt.Errorf("CTest discovery produced no JSON")
	}
	var document ctestJSON
	if err := json.Unmarshal(data, &document); err != nil {
		return Discovery{}, fmt.Errorf("parse CTest discovery JSON: %w", err)
	}
	allowed := make(map[string]struct{}, len(project.AllowedTests))
	for _, name := range project.AllowedTests {
		allowed[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(project.AllowedTests))
	found := make([]string, 0, len(project.AllowedTests))
	missing := make([]string, 0)
	for _, test := range document.Tests {
		name := test.Name
		if _, ok := allowed[name]; !ok {
			// Unrelated malformed/duplicate names cannot match an allowlisted
			// test; ignore them rather than failing the whole discovery.
			continue
		}
		if name == "" || name != strings.TrimSpace(name) ||
			strings.ContainsFunc(name, unicode.IsControl) ||
			len(name) > config.MaxCTestNameBytes {
			return Discovery{}, fmt.Errorf("CTest discovery returned an invalid allowlisted test name")
		}
		if _, duplicate := seen[name]; duplicate {
			return Discovery{}, fmt.Errorf("CTest discovery returned duplicate allowlisted test %q", name)
		}
		seen[name] = struct{}{}
		found = append(found, name)
	}
	for _, name := range project.AllowedTests {
		if _, ok := seen[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(found)
	sort.Strings(missing)
	return Discovery{
		SchemaVersion: SchemaVersion,
		ProjectID:     project.ID,
		Tests:         found,
		Missing:       missing,
		Complete:      len(missing) == 0,
		Warnings:      discoveryWarnings(missing),
	}, nil
}

func discoveryWarnings(missing []string) []string {
	if len(missing) == 0 {
		return []string{}
	}
	names := append([]string(nil), missing...)
	if len(names) > 16 {
		names = names[:16]
	}
	return []string{"configured tests were not discovered: " + strings.Join(names, ", ")}
}
