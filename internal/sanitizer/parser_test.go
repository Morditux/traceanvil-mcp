package sanitizer

import (
	"strings"
	"testing"
)

func TestParseSanitizerLogs(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
		log  string
		kind string
	}{
		{
			name: "asan", tool: ToolASan,
			log: `==19==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1
WRITE of size 4 at 0x1 thread T0
    #0 0x5d9 in main /src/asan.c:2
    #1 0x7bf in __libc_start_main /src/libc.c:1
SUMMARY: AddressSanitizer: heap-buffer-overflow /src/asan.c:2 in main`,
			kind: "AddressSanitizer/heap-buffer-overflow",
		},
		{
			name: "ubsan", tool: ToolUBSan,
			log:  `/src/ubsan.c:2:40: runtime error: signed integer overflow: 1 + 1 cannot be represented in type 'int'`,
			kind: "UndefinedBehavior",
		},
		{
			name: "lsan", tool: ToolLSan,
			log: `==25==ERROR: LeakSanitizer: detected memory leaks
Direct leak of 123 byte(s) in 1 object(s) allocated from:
    #0 0x76 in malloc /src/lsan.c:2
SUMMARY: LeakSanitizer: 123 byte(s) leaked in 1 allocation(s).`,
			kind: "Leak_Direct",
		},
		{
			name: "tsan", tool: ToolTSan,
			log: `==================
WARNING: ThreadSanitizer: data race (pid=4)
  Read of size 4 at 0x1 by thread T2:
    #0 worker /src/tsan.c:3 (fixture+0x1)
  Previous write of size 4 at 0x1 by thread T1:
    #0 worker /src/tsan.c:3 (fixture+0x1)
SUMMARY: ThreadSanitizer: data race /src/tsan.c:3 in worker`,
			kind: "DataRace",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Parse(test.tool, strings.NewReader(test.log), ParseOptions{})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !result.Recognized || len(result.Issues) == 0 {
				t.Fatalf("result = %+v", result)
			}
			if result.Issues[0].Kind != test.kind {
				t.Errorf("kind = %q, want %q", result.Issues[0].Kind, test.kind)
			}
			if result.Issues[0].Severity == "" {
				t.Error("severity is empty")
			}
			if len(result.Issues[0].Stack) == 0 {
				t.Errorf("stack is empty: %+v", result.Issues[0])
			}
			first := result.Issues[0].Stack[0]
			if first.Path == "" || strings.Contains(first.Path, " ") {
				t.Errorf("first frame = %+v", first)
			}
			if test.tool == ToolTSan && len(result.Issues[0].PreviousStack) == 0 {
				t.Errorf("TSan previous-access stack is empty: %+v", result.Issues[0])
			}
		})
	}
}

func TestSanitizerRuntimeNoticeIsNotClean(t *testing.T) {
	for _, test := range []struct {
		tool Tool
		log  string
	}{
		{ToolASan, "AddressSanitizer: failed to intercept '__isoc99_printf'"},
		{ToolTSan, "ThreadSanitizer: memory layout is incompatible"},
		{ToolTSan, "ThreadSanitizer: reported 1 warnings"},
	} {
		result, err := Parse(test.tool, strings.NewReader(test.log), ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Incomplete {
			t.Errorf("%s notice was treated as clean evidence: %+v", test.tool, result)
		}
	}
}

func TestParseUnknownOrTruncatedLogIsNotClean(t *testing.T) {
	result, err := Parse(ToolASan, strings.NewReader("some user output\n"), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Incomplete || result.Recognized {
		t.Fatalf("unknown log = %+v", result)
	}
	result, err = Parse(ToolASan, strings.NewReader(strings.Repeat("x", 100)+"\n"), ParseOptions{MaxBytes: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.Incomplete {
		t.Fatalf("truncated log = %+v", result)
	}
}

func TestLSanLeaksAfterEmptyStackAreKept(t *testing.T) {
	log := `ERROR: LeakSanitizer: detected memory leaks
Direct leak of 10 byte(s) in 1 object(s) allocated from:
<empty stack>
Direct leak of 20 byte(s) in 1 object(s) allocated from:
    #0 0x1 in main /src/leak.c:2
SUMMARY: LeakSanitizer: 30 byte(s) leaked in 2 allocation(s).`
	result, err := Parse(ToolLSan, strings.NewReader(log), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("issues = %+v", result.Issues)
	}
	if result.Issues[0].LeakedBytes != 10 || len(result.Issues[0].Stack) != 0 {
		t.Errorf("first issue = %+v", result.Issues[0])
	}
	if result.Issues[1].LeakedBytes != 20 || len(result.Issues[1].Stack) == 0 {
		t.Errorf("second issue = %+v", result.Issues[1])
	}
}

func TestUnsymbolizedFrameDoesNotKeepInstructionAddress(t *testing.T) {
	log := `ERROR: AddressSanitizer: SEGV on unknown address 0x1
    #0 0x55d3b1 (/p/a.out+0x1234)
SUMMARY: AddressSanitizer: SEGV`
	result, err := Parse(ToolASan, strings.NewReader(log), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 1 || len(result.Issues[0].Stack) != 1 {
		t.Fatalf("result = %+v", result)
	}
	frame := result.Issues[0].Stack[0]
	if strings.Contains(frame.Function, "0x55d3b1") {
		t.Errorf("frame retained an instruction address: %+v", frame)
	}
	if frame.Object == "" {
		t.Errorf("frame lost its object: %+v", frame)
	}
}
