package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// setStamp replaces the package-level stamps for the duration of a test.
func setStamp(t *testing.T, v, c, d string) {
	t.Helper()

	oldVersion, oldCommit, oldDate := version, commit, date
	version, commit, date = v, c, d
	t.Cleanup(func() {
		version, commit, date = oldVersion, oldCommit, oldDate
	})
}

func TestCurrentUsesLinkTimeStamps(t *testing.T) {
	setStamp(t, "1.2.3", "abc1234", "2026-10-18T00:00:00Z")

	got := Current()
	if got.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", got.Version, "1.2.3")
	}
	if got.Commit != "abc1234" {
		t.Errorf("Commit = %q, want %q", got.Commit, "abc1234")
	}
	if got.Date != "2026-10-18T00:00:00Z" {
		t.Errorf("Date = %q, want %q", got.Date, "2026-10-18T00:00:00Z")
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestCurrentNeverReturnsEmptyFields(t *testing.T) {
	setStamp(t, "0.0.0-dev", "unknown", "unknown")

	got := Current()
	if got.Version == "" || got.Commit == "" || got.Date == "" || got.GoVersion == "" {
		t.Fatalf("Current() returned an empty field: %+v", got)
	}
	if got.Commit == "" {
		t.Fatal("Commit must fall back to a non-empty value")
	}
}

func TestStringIsStable(t *testing.T) {
	setStamp(t, "1.2.3", "abc1234", "2026-10-18T00:00:00Z")

	want := Binary + " 1.2.3 (commit abc1234, date 2026-10-18T00:00:00Z, " + runtime.Version() + ")"
	if got := Current().String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringMarksDirtyBuilds(t *testing.T) {
	info := Info{
		Version:   "1.2.3",
		Commit:    "abc1234",
		Date:      "2026-10-18T00:00:00Z",
		GoVersion: runtime.Version(),
		Modified:  true,
	}

	want := Binary + " 1.2.3 (commit abc1234-dirty, date 2026-10-18T00:00:00Z, " + runtime.Version() + ")"
	if got := info.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringDoesNotSuffixUnknownCommit(t *testing.T) {
	info := Info{
		Version:   "1.2.3",
		Commit:    "unknown",
		Date:      "unknown",
		GoVersion: runtime.Version(),
		Modified:  true,
	}

	if got := info.String(); !strings.Contains(got, "commit unknown,") {
		t.Errorf("String() = %q, want an unsuffixed unknown commit", got)
	}
}

func TestParseVCSMetadata(t *testing.T) {
	tests := []struct {
		name         string
		settings     []debug.BuildSetting
		wantCommit   string
		wantDate     string
		wantModified bool
	}{
		{
			name:       "no settings",
			settings:   nil,
			wantCommit: "unknown",
			wantDate:   "unknown",
		},
		{
			name: "complete and dirty",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc1234"},
				{Key: "vcs.time", Value: "2026-10-18T00:00:00Z"},
				{Key: "vcs.modified", Value: "true"},
			},
			wantCommit:   "abc1234",
			wantDate:     "2026-10-18T00:00:00Z",
			wantModified: true,
		},
		{
			name: "complete and clean",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc1234"},
				{Key: "vcs.time", Value: "2026-10-18T00:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			},
			wantCommit: "abc1234",
			wantDate:   "2026-10-18T00:00:00Z",
		},
		{
			name: "empty values fall back to unknown",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: ""},
				{Key: "vcs.time", Value: ""},
			},
			wantCommit: "unknown",
			wantDate:   "unknown",
		},
		{
			name: "unrelated settings are ignored",
			settings: []debug.BuildSetting{
				{Key: "GOARCH", Value: "amd64"},
				{Key: "vcs.revision", Value: "abc1234"},
			},
			wantCommit: "abc1234",
			wantDate:   "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commit, date, modified := parseVCSMetadata(tt.settings)

			if commit != tt.wantCommit {
				t.Errorf("commit = %q, want %q", commit, tt.wantCommit)
			}
			if date != tt.wantDate {
				t.Errorf("date = %q, want %q", date, tt.wantDate)
			}
			if modified != tt.wantModified {
				t.Errorf("modified = %t, want %t", modified, tt.wantModified)
			}
		})
	}
}
