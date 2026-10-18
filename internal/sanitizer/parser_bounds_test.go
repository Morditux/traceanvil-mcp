package sanitizer

import (
	"strings"
	"testing"
)

func TestParseRejectsInvalidInputs(t *testing.T) {
	if _, err := Parse(ToolASan, nil, ParseOptions{}); err == nil {
		t.Fatal("Parse(nil reader) accepted")
	}
	if _, err := Parse("msan", strings.NewReader(""), ParseOptions{}); err == nil {
		t.Fatal("Parse(msan) accepted")
	}
}

func TestParseLineAndStringBounds(t *testing.T) {
	result, err := Parse(ToolASan, strings.NewReader("one\ntwo\nthree\n"), ParseOptions{MaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.Incomplete {
		t.Fatalf("line bound result = %+v", result)
	}
	long := strings.Repeat("x", 100)
	result, err = Parse(ToolASan, strings.NewReader(long+"\n"), ParseOptions{MaxStringSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.Incomplete {
		t.Fatalf("string bound result = %+v", result)
	}
}

func TestParseIssueAndFrameBounds(t *testing.T) {
	log := "ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\n" +
		"    #0 a /src/a.c:1\n    #1 b /src/b.c:2\n    #2 c /src/c.c:3\n"
	result, err := Parse(ToolASan, strings.NewReader(log), ParseOptions{MaxFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 1 || len(result.Issues[0].Stack) != 1 {
		t.Fatalf("frame bound = %+v", result.Issues)
	}

	var many strings.Builder
	for i := 0; i < 5; i++ {
		many.WriteString("ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\n")
	}
	result, err = Parse(ToolASan, strings.NewReader(many.String()), ParseOptions{MaxIssues: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Issues) != 2 {
		t.Fatalf("issue bound = %+v", result)
	}
}
