package valgrind

import (
	"strings"
	"testing"
)

func TestOptionsDefaults(t *testing.T) {
	options := DefaultOptions()
	options.LeakCheck = ""
	options.ShowLeakKinds = ""
	options.NumCallers = 0
	if err := options.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if options.LeakCheck != LeakCheckFull || options.ShowLeakKinds != ShowLeakKindsAll ||
		options.NumCallers != DefaultNumCallers || !options.TrackOrigins {
		t.Errorf("defaults = %+v", options)
	}
}

func TestOptionsRejections(t *testing.T) {
	cases := []struct {
		name    string
		options Options
	}{
		{"leak_check", Options{LeakCheck: "all"}},
		{"show_leak_kinds", Options{ShowLeakKinds: "definite,indirect"}},
		{"num_callers low", Options{NumCallers: -1}},
		{"num_callers high", Options{NumCallers: MaxNumCallers + 1}},
		{"trace_children", Options{TraceChildren: true}},
		{"suppressions NUL", Options{SuppressionsProfile: "a\x00b"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.options.Validate(); err == nil {
				t.Fatal("Validate() accepted an invalid option")
			}
		})
	}
}

func TestOptionsArguments(t *testing.T) {
	options := DefaultOptions()
	options.SuppressionsProfile = "addr4"
	args, err := options.Arguments(
		"/reports/run_0123456789abcdef0123456789abcdef/memcheck.xml",
		"/ws/supp/addr4.supp",
		"/ws/build/fixture",
		[]string{"--input", "fixture.dat"},
	)
	if err != nil {
		t.Fatalf("Arguments() error = %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, expected := range []string{
		"--tool=memcheck",
		"--xml=yes",
		"--xml-file=/reports/run_0123456789abcdef0123456789abcdef/memcheck.xml",
		"--error-exitcode=42",
		"--leak-check=full",
		"--show-leak-kinds=all",
		"--track-origins=yes",
		"--num-callers=12",
		"--suppressions=/ws/supp/addr4.supp",
		"--",
		"/ws/build/fixture",
		"--input",
		"fixture.dat",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("arguments missing %q:\n%s", expected, joined)
		}
	}
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("arguments have no -- separator")
	}
	if args[separator+1] != "/ws/build/fixture" {
		t.Errorf("program follows -- as %q", args[separator+1])
	}
	if args[len(args)-1] != "fixture.dat" {
		t.Errorf("last argument = %q, want the program argument", args[len(args)-1])
	}
}

func TestOptionsArgumentsDoNotAllowFlagInjection(t *testing.T) {
	options := DefaultOptions()
	args, err := options.Arguments("/reports/x/memcheck.xml", "", "/ws/build/fixture",
		[]string{"--tool=helgrind", "--xml-file=/tmp/escape"})
	if err != nil {
		t.Fatalf("Arguments() error = %v", err)
	}
	// The program path and its arguments must appear only after "--".
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("arguments have no -- separator")
	}
	for _, arg := range args[:separator] {
		if arg == "--tool=helgrind" || arg == "--xml-file=/tmp/escape" {
			t.Errorf("program argument leaked into Valgrind options: %q", arg)
		}
	}
	if args[separator+2] != "--tool=helgrind" || args[separator+3] != "--xml-file=/tmp/escape" {
		t.Errorf("arguments after -- = %v", args[separator+1:])
	}
}

func TestOptionsArgumentsRejectUnsafePaths(t *testing.T) {
	options := DefaultOptions()
	cases := []struct {
		name string
		xml  string
		supp string
		prog string
	}{
		{"relative xml", "memcheck.xml", "", "/ws/fixture"},
		{"relative program", "/reports/x.xml", "", "fixture"},
		{"nul xml", "/reports/x\x00.xml", "", "/ws/fixture"},
		{"relative suppression", "/reports/x.xml", "supp", "/ws/fixture"},
		{"missing suppression path", "/reports/x.xml", "", "/ws/fixture"},
	}
	options.SuppressionsProfile = "addr4"
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := options.Arguments(test.xml, test.supp, test.prog, nil); err == nil {
				t.Fatal("Arguments() accepted an unsafe path")
			}
		})
	}
}

func TestOptionsMapIsComplete(t *testing.T) {
	options := DefaultOptions()
	options.SuppressionsProfile = "addr4"
	values := options.Map()
	for _, key := range []string{
		"leak_check", "show_leak_kinds", "track_origins", "num_callers",
		"suppressions_profile", "trace_children",
	} {
		if _, ok := values[key]; !ok {
			t.Errorf("Map() is missing %q", key)
		}
	}
	if values["trace_children"] != false {
		t.Errorf("trace_children = %v", values["trace_children"])
	}
}
