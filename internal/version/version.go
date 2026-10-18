// Package version exposes build information for the server binary.
//
// Values are overridable at link time:
//
//	go build -ldflags "\
//	  -X github.com/Morditux/traceanvil-mcp/internal/version.version=1.2.3 \
//	  -X github.com/Morditux/traceanvil-mcp/internal/version.commit=abc1234 \
//	  -X github.com/Morditux/traceanvil-mcp/internal/version.date=2026-10-18T00:00:00Z"
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Binary is the canonical command name used in user-facing output.
const Binary = "mcp-native-diagnostics"

// Defaults are intentionally non-empty so that an unstamped development build
// never reports misleading information. Link-time stamps take precedence. The
// repository's CI does not stamp release artifacts yet; that happens at J14.
var (
	version = "0.0.0-dev"
	commit  = "unknown"
	date    = "unknown"
)

// Info is the immutable build description returned by Current.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Modified  bool   `json:"modified"`
}

// Current returns build information. It reads only in-memory build metadata and
// never executes an external command or touches the filesystem.
func Current() Info {
	vcsCommit, vcsDate, vcsModified := vcsMetadata()

	currentCommit := commit
	if currentCommit == "unknown" {
		currentCommit = vcsCommit
	}
	currentDate := date
	if currentDate == "unknown" {
		currentDate = vcsDate
	}

	return Info{
		Version:   version,
		Commit:    currentCommit,
		Date:      currentDate,
		GoVersion: runtime.Version(),
		Modified:  vcsModified,
	}
}

// String renders a single stable line suitable for CLI output. A build produced
// from a modified checkout is marked with a "-dirty" commit suffix so that
// provenance is never overstated.
func (i Info) String() string {
	commit := i.Commit
	if i.Modified && commit != "unknown" {
		commit += "-dirty"
	}

	// "date" is the link-time build date when stamped, otherwise the VCS commit
	// time. It is deliberately not labelled "built" to avoid overstating it.
	return fmt.Sprintf("%s %s (commit %s, date %s, %s)",
		Binary, i.Version, commit, i.Date, i.GoVersion)
}

// vcsMetadata extracts revision information stamped by the Go toolchain when
// the binary is built inside a version control checkout.
func vcsMetadata() (string, string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown", "unknown", false
	}

	return parseVCSMetadata(info.Settings)
}

// parseVCSMetadata is separated from debug.ReadBuildInfo so its handling of
// missing or partial settings can be tested directly.
func parseVCSMetadata(settings []debug.BuildSetting) (string, string, bool) {
	commit, date := "unknown", "unknown"
	modified := false

	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				commit = setting.Value
			}
		case "vcs.time":
			if setting.Value != "" {
				date = setting.Value
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	return commit, date, modified
}
