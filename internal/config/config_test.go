package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validJSON() string {
	return `{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": [
			{"id": "fixture", "program": "/tmp/ws/build/fixture", "cwd": "/tmp/ws", "allowed_args": ["--one"], "sanitizers": ["asan", "tsan"]}
		],
		"ctest": {
			"projects": [
				{"id": "tests", "test_dir": "/tmp/ws/build", "allowed_tests": ["pass", "fail"],
				 "profiles": {"quick": ["pass"]}}
			],
			"test_timeout_seconds": 30
		},
		"backends": {"valgrind": true, "gdb": false, "lldb": false, "sanitizers": true, "ctest": true},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_sessions": 1,
		           "max_report_bytes": 4096, "max_response_items": 10}
	}`
}

func TestParseValid(t *testing.T) {
	cfg, err := Parse(strings.NewReader(validJSON()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, SchemaVersion)
	}
	if got := cfg.AllowedTargets[0].Program; got != "/tmp/ws/build/fixture" {
		t.Errorf("Program = %q, want the cleaned absolute path", got)
	}
	if !cfg.Backends.Valgrind || cfg.Backends.GDB || cfg.Backends.LLDB || !cfg.Backends.Sanitizers || !cfg.Backends.CTest {
		t.Errorf("backends = %+v, want valgrind, sanitizers and ctest only", cfg.Backends)
	}
	if len(cfg.AllowedTargets[0].Sanitizers) != 2 {
		t.Errorf("sanitizers = %v", cfg.AllowedTargets[0].Sanitizers)
	}
	if len(cfg.CTest.Projects) != 1 || cfg.CTest.Projects[0].TestDir != "/tmp/ws/build" {
		t.Errorf("ctest = %+v", cfg.CTest)
	}
	if cfg.Policy.Profile != ProfileSafeDefault {
		t.Errorf("Profile = %q, want %q", cfg.Policy.Profile, ProfileSafeDefault)
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	const doc = `{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": []
	}`

	cfg, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Policy.Profile != ProfileSafeDefault {
		t.Errorf("Profile = %q, want %q", cfg.Policy.Profile, ProfileSafeDefault)
	}
	if cfg.Limits.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Errorf("TimeoutSeconds = %d, want %d", cfg.Limits.TimeoutSeconds, DefaultTimeoutSeconds)
	}
	if cfg.Limits.MaxJobs != DefaultMaxJobs {
		t.Errorf("MaxJobs = %d, want %d", cfg.Limits.MaxJobs, DefaultMaxJobs)
	}
	if cfg.Limits.MaxSessions != DefaultMaxSessions {
		t.Errorf("MaxSessions = %d, want %d", cfg.Limits.MaxSessions, DefaultMaxSessions)
	}
	if cfg.Limits.MaxReportBytes != DefaultMaxReportBytes {
		t.Errorf("MaxReportBytes = %d, want %d", cfg.Limits.MaxReportBytes, DefaultMaxReportBytes)
	}
	if cfg.Limits.MaxResponseItems != DefaultMaxResponseItems {
		t.Errorf("MaxResponseItems = %d, want %d", cfg.Limits.MaxResponseItems, DefaultMaxResponseItems)
	}
	if cfg.CTest.TestTimeoutSeconds != DefaultCTestTimeout {
		t.Errorf("CTest timeout = %d, want %d", cfg.CTest.TestTimeoutSeconds, DefaultCTestTimeout)
	}
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{name: "empty", doc: "", wantErr: "empty file"},
		{name: "whitespace only", doc: " \n\t", wantErr: "empty file"},
		{name: "bad JSON", doc: `{"schema_version":`, wantErr: "invalid document"},
		{name: "unknown field", doc: `{"schema_version":1,"unexpected":true}`, wantErr: "unknown field"},
		{name: "trailing value", doc: validJSON() + `{}`, wantErr: "trailing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("Parse() error = nil, want an error")
			}
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error type = %T, want *ParseError (%v)", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseRejectsOversizedDocument(t *testing.T) {
	doc := strings.Repeat(" ", MaxFileBytes) + validJSON()
	_, err := Parse(strings.NewReader(doc))
	if err == nil {
		t.Fatal("Parse() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want a size error", err)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "schema version",
			mutate:  func(c *Config) { c.SchemaVersion = 2 },
			wantErr: "schema_version",
		},
		{
			name:    "no workspace roots",
			mutate:  func(c *Config) { c.WorkspaceRoots = nil },
			wantErr: "at least one root",
		},
		{
			name:    "relative root",
			mutate:  func(c *Config) { c.WorkspaceRoots[0] = "relative/root" },
			wantErr: "absolute path",
		},
		{
			name:    "filesystem root",
			mutate:  func(c *Config) { c.WorkspaceRoots[0] = "/" },
			wantErr: "filesystem root",
		},
		{
			name:    "proc root",
			mutate:  func(c *Config) { c.WorkspaceRoots[0] = "/proc/self" },
			wantErr: "/proc",
		},
		{
			name:    "duplicate root",
			mutate:  func(c *Config) { c.WorkspaceRoots = []string{"/tmp/ws", "/tmp/ws/"} },
			wantErr: "duplicate root",
		},
		{
			name:    "relative report dir",
			mutate:  func(c *Config) { c.ReportDir = "reports" },
			wantErr: "report_dir",
		},
		{
			name:    "device report dir",
			mutate:  func(c *Config) { c.ReportDir = "/dev/shm/reports" },
			wantErr: "/dev",
		},
		{
			name:    "target outside roots",
			mutate:  func(c *Config) { c.AllowedTargets[0].Program = "/tmp/other/fixture" },
			wantErr: "inside workspace_roots",
		},
		{
			name:    "cwd outside roots",
			mutate:  func(c *Config) { c.AllowedTargets[0].Cwd = "/tmp/other" },
			wantErr: "inside workspace_roots",
		},
		{
			name:    "traversal target",
			mutate:  func(c *Config) { c.AllowedTargets[0].Program = "/tmp/ws/../other/fixture" },
			wantErr: "inside workspace_roots",
		},
		{
			name:    "invalid target id",
			mutate:  func(c *Config) { c.AllowedTargets[0].ID = "Fixture With Spaces" },
			wantErr: "allowed_targets[0].id",
		},
		{
			name: "duplicate target id",
			mutate: func(c *Config) {
				c.AllowedTargets = append(c.AllowedTargets, c.AllowedTargets[0])
			},
			wantErr: "duplicate target id",
		},
		{
			name:    "NUL in argument",
			mutate:  func(c *Config) { c.AllowedTargets[0].AllowedArgs = []string{"ok", "bad\x00arg"} },
			wantErr: "NUL",
		},
		{
			name:    "argument too long",
			mutate:  func(c *Config) { c.AllowedTargets[0].AllowedArgs = []string{strings.Repeat("a", MaxArgBytes+1)} },
			wantErr: "at most",
		},
		{
			name:    "unknown sanitizer",
			mutate:  func(c *Config) { c.AllowedTargets[0].Sanitizers = []string{"msan"} },
			wantErr: "asan, ubsan, lsan or tsan",
		},
		{
			name:    "duplicate sanitizer",
			mutate:  func(c *Config) { c.AllowedTargets[0].Sanitizers = []string{"asan", "asan"} },
			wantErr: "duplicate sanitizer",
		},
		{
			name: "ctest project outside roots",
			mutate: func(c *Config) {
				c.CTest.Projects[0].TestDir = "/tmp/outside"
			},
			wantErr: "inside workspace_roots",
		},
		{
			name: "ctest profile unknown test",
			mutate: func(c *Config) {
				c.CTest.Projects[0].Profiles["quick"] = []string{"unknown"}
			},
			wantErr: "not in allowed_tests",
		},
		{
			name: "ctest duplicate allowed test",
			mutate: func(c *Config) {
				c.CTest.Projects[0].AllowedTests = []string{"pass", "pass"}
			},
			wantErr: "duplicate test name",
		},
		{
			name:    "invalid profile",
			mutate:  func(c *Config) { c.Policy.Profile = "root" },
			wantErr: "policy.profile",
		},
		{
			name:    "attach in safe profile",
			mutate:  func(c *Config) { c.Policy.Attach = true },
			wantErr: "elevated-local",
		},
		{
			name:    "evaluate in trusted profile",
			mutate:  func(c *Config) { c.Policy.Profile = ProfileTrustedLocal; c.Policy.Evaluate = true },
			wantErr: "elevated-local",
		},
		{
			name:    "timeout too low",
			mutate:  func(c *Config) { c.Limits.TimeoutSeconds = -1 },
			wantErr: "limits.timeout_seconds",
		},
		{
			name:    "jobs too high",
			mutate:  func(c *Config) { c.Limits.MaxJobs = MaxMaxJobs + 1 },
			wantErr: "limits.max_jobs",
		},
		{
			name:    "report bytes too low",
			mutate:  func(c *Config) { c.Limits.MaxReportBytes = 10 },
			wantErr: "limits.max_report_bytes",
		},
		{
			name:    "response items too high",
			mutate:  func(c *Config) { c.Limits.MaxResponseItems = MaxResponseItems + 1 },
			wantErr: "limits.max_response_items",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, validJSON())
			tt.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type = %T, want *ValidationError (%v)", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAcceptsElevatedProfile(t *testing.T) {
	cfg := mustParse(t, validJSON())
	cfg.Policy.Profile = ProfileElevatedLocal
	cfg.Policy.Attach = true
	cfg.Policy.Evaluate = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validJSON()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, SchemaVersion)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(dir, "absent.json"))
		if err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
		var loadErr *LoadError
		if !errors.As(err, &loadErr) {
			t.Fatalf("error type = %T, want *LoadError (%v)", err, err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		_, err := Load(dir)
		if err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})

	t.Run("oversized file", func(t *testing.T) {
		path := filepath.Join(dir, "big.json")
		if err := os.WriteFile(path, make([]byte, MaxFileBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error = %q, want a size error", err)
		}
	})
}

func mustParse(t *testing.T, doc string) *Config {
	t.Helper()

	cfg, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return cfg
}

func ExampleParse() {
	cfg, err := Parse(strings.NewReader(validJSON()))
	if err != nil {
		fmt.Println("invalid:", err)
		return
	}
	fmt.Println(cfg.Policy.Profile, cfg.AllowedTargets[0].ID)
	// Output: safe-default fixture
}

func TestParseValgrindSuppressions(t *testing.T) {
	const doc = `{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": [],
		"valgrind": {
			"suppressions": {
				"addr4": "/tmp/ws/supp/addr4.supp",
				"hidden-leaks": "/tmp/ws/supp/leaks.supp"
			}
		}
	}`
	cfg, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := cfg.Valgrind.Suppressions["addr4"]; got != "/tmp/ws/supp/addr4.supp" {
		t.Errorf("addr4 = %q", got)
	}
	if len(cfg.Valgrind.Suppressions) != 2 {
		t.Errorf("len(Suppressions) = %d, want 2", len(cfg.Valgrind.Suppressions))
	}
}

func TestParseValgrindSuppressionsRejections(t *testing.T) {
	base := `{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": [],
		"valgrind": {"suppressions": {%s}}
	}`
	cases := []struct {
		name  string
		entry string
	}{
		{"outside roots", `"addr4": "/tmp/elsewhere/addr4.supp"`},
		{"relative path", `"addr4": "supp/addr4.supp"`},
		{"invalid profile id", `"../escape": "/tmp/ws/addr4.supp"`},
		{"sensitive path", `"proc": "/proc/self/maps"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(fmt.Sprintf(base, test.entry)))
			if err == nil {
				t.Fatal("Parse() accepted an invalid suppression entry")
			}
		})
	}
}

func TestParseValgrindSuppressionsCardinality(t *testing.T) {
	entries := make([]string, 0, MaxSuppressionProfiles+1)
	for i := 0; i < MaxSuppressionProfiles+1; i++ {
		entries = append(entries, fmt.Sprintf(`"profile-%02d": "/tmp/ws/profile-%02d.supp"`, i, i))
	}
	doc := fmt.Sprintf(`{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": [],
		"valgrind": {"suppressions": {%s}}
	}`, strings.Join(entries, ","))
	if _, err := Parse(strings.NewReader(doc)); err == nil {
		t.Fatal("Parse() accepted too many suppression profiles")
	}
}

func TestParseAttachAndCoreProfiles(t *testing.T) {
	doc := `{
		"schema_version": 1,
		"workspace_roots": ["/tmp/ws"],
		"report_dir": "/tmp/ws-reports",
		"allowed_targets": [],
		"backends": {"gdb": true},
		"policy": {"profile": "elevated-local", "attach": true, "evaluate": false,
		           "attach_allowed_pids": [1234, 5678]},
		"cores": [{"id": "crash", "executable": "/tmp/ws/build/fixture", "core": "/tmp/ws/core"}],
		"limits": {}
	}`
	cfg, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(cfg.Policy.AttachAllowedPIDs) != 2 || len(cfg.Cores) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Cores[0].Core != "/tmp/ws/core" {
		t.Errorf("core = %q", cfg.Cores[0].Core)
	}
}
