package profile

import (
	"strings"
	"testing"
)

func TestParseCallgrindNativeFormat(t *testing.T) {
	input := `# callgrind format
version: 1
creator: callgrind-3.26.0
positions: line
events: Ir Dr
summary: 350 35

fl=(1) main.c
fn=(1) main
10 100 10
20 50 5

fl=(2) work.c
fn=(2) work
30 200 20
`
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0] != "Ir" || result.Events[1] != "Dr" {
		t.Fatalf("events = %v", result.Events)
	}
	if len(result.Totals) != 2 || result.Totals[0] != 350 || result.Totals[1] != 35 {
		t.Fatalf("totals = %v", result.Totals)
	}
	if result.FunctionCount != 2 {
		t.Fatalf("functions = %d", result.FunctionCount)
	}
	if len(result.TopFunctions) != 2 || result.TopFunctions[0].Name != "work" ||
		result.TopFunctions[0].Self != 200 || result.TopFunctions[0].Event != "Ir" {
		t.Fatalf("top = %+v", result.TopFunctions)
	}
	if result.TopFunctions[1].File != "main.c" {
		t.Fatalf("top file = %+v", result.TopFunctions[1])
	}
}

func TestParseCallgrindCallCostsAndIdCompression(t *testing.T) {
	input := `events: Ir
fl=(1) main.c
fn=(1) main
10 100
cfn=(2) work
calls=1 20
* 50
20 10
fl=(2) work.c
fn=(2) work
5 50
fn=(1)
30 5
`
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.FunctionCount != 2 {
		t.Fatalf("functions = %d", result.FunctionCount)
	}
	if len(result.TopFunctions) != 2 {
		t.Fatalf("top = %+v", result.TopFunctions)
	}
	if result.TopFunctions[0].Name != "main" || result.TopFunctions[0].Self != 115 ||
		result.TopFunctions[0].File != "main.c" {
		t.Fatalf("top main = %+v", result.TopFunctions[0])
	}
	if result.TopFunctions[1].Name != "work" || result.TopFunctions[1].Self != 50 ||
		result.TopFunctions[1].File != "work.c" {
		t.Fatalf("top work = %+v", result.TopFunctions[1])
	}
}

func TestParseCallgrindInlineFilesAndCallDefinitions(t *testing.T) {
	input := `events: Ir
fl=(1) main.c
fn=(1) main
10 10
fi=(2) inline.h
20 5
fe=(1) main.c
30 5
cfi=(3) work.c
cfn=(2) work
calls=1 1
* 99
fn=(2)
40 5
`
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.FunctionCount != 2 {
		t.Fatalf("functions = %d", result.FunctionCount)
	}
	if len(result.TopFunctions) != 2 ||
		result.TopFunctions[0].Name != "main" || result.TopFunctions[0].Self != 20 ||
		result.TopFunctions[0].File != "main.c" {
		t.Fatalf("main top = %+v", result.TopFunctions)
	}
	if result.TopFunctions[1].Name != "work" || result.TopFunctions[1].Self != 5 ||
		result.TopFunctions[1].File != "work.c" {
		t.Fatalf("work top = %+v", result.TopFunctions)
	}
}

func TestParseCallgrindCallTargetsDoNotLeakFiles(t *testing.T) {
	input := `events: Ir
fl=(1) main.c
fn=(1) main
10 10
cfl=(2) lib.c
cfn=(2) f1
calls=1 1
* 9
cfn=(3) f2
calls=1 1
* 9
fn=(3)
40 5
`
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TopFunctions) != 2 {
		t.Fatalf("top = %+v", result.TopFunctions)
	}
	if result.TopFunctions[0].Name != "main" || result.TopFunctions[0].File != "main.c" {
		t.Fatalf("top = %+v", result.TopFunctions)
	}
	if result.TopFunctions[1].Name != "f2" || result.TopFunctions[1].File != "main.c" {
		t.Fatalf("f2 top = %+v", result.TopFunctions)
	}
}

func TestParseCallgrindNamedFunctionAfterInlineFile(t *testing.T) {
	input := `events: Ir
fl=(1) main.c
fn=(1) main
10 10
fi=(2) inline.h
20 5
fn=(2) inline_work
30 5
`
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TopFunctions) != 2 {
		t.Fatalf("top = %+v", result.TopFunctions)
	}
	byName := map[string]FunctionCost{}
	for _, function := range result.TopFunctions {
		byName[function.Name] = function
	}
	if byName["main"].Self != 15 || byName["main"].File != "main.c" {
		t.Fatalf("main top = %+v", byName["main"])
	}
	if byName["inline_work"].File != "inline.h" {
		t.Fatalf("inline top = %+v", byName["inline_work"])
	}
}

func TestParseCallgrindWithoutSummarySumsFunctions(t *testing.T) {
	input := "events: Ir\nfl=a.c\nfn=main\n1 10\n2 5\n"
	result, err := ParseCallgrind(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Totals) != 1 || result.Totals[0] != 15 {
		t.Fatalf("totals = %v", result.Totals)
	}
}

func TestParseCallgrindRejectsMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"no events":  "fn=main\n1 1\n",
		"bad cost":   "events: Ir\nfn=main\n1 nope\n",
		"few values": "events: Ir Dr\nfn=main\n1 1\n",
		"nan":        "events: Ir\nfn=main\n1 NaN\n",
		"negative":   "events: Ir\nfn=main\n1 -1\n",
		"cost first": "events: Ir\n1 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCallgrind(strings.NewReader(input)); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestParseMassifNativeFormat(t *testing.T) {
	input := `desc: --massif-out-file=massif.out
cmd: ./fixture
time_unit: i
#-----------
snapshot=0
#-----------
time=0
mem_heap_B=0
mem_heap_extra_B=0
mem_stacks_B=0
heap_tree=empty
#-----------
snapshot=1
#-----------
time=100
mem_heap_B=1048576
mem_heap_extra_B=8
mem_stacks_B=0
heap_tree=peak
n1: 1048576 (heap allocation functions) malloc/new/new[]
 n0: 1048576 0x1: main (fixture.c:10)
#-----------
snapshot=2
#-----------
time=200
mem_heap_B=0
mem_heap_extra_B=0
mem_stacks_B=0
heap_tree=empty
`
	result, err := ParseMassif(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.TimeUnit != "i" || result.Snapshots != 3 {
		t.Fatalf("result = %+v", result)
	}
	if result.PeakHeap != 1048576 || result.PeakExtra != 8 {
		t.Fatalf("peak = %+v", result)
	}
	if result.PeakIndex != 1 || result.PeakSnapshot.HeapBytes != 1048576 {
		t.Fatalf("peak index = %d snapshot=%+v", result.PeakIndex, result.PeakSnapshot)
	}
	if len(result.Samples) != 3 || !result.Samples[1].Peak || result.Samples[0].Peak || result.Samples[2].Peak {
		t.Fatalf("samples = %+v", result.Samples)
	}
}

func TestParseMassifMissingTimeUnitIsSurfaced(t *testing.T) {
	result, err := ParseMassif(strings.NewReader("snapshot=1\nmem_heap_B=10\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.TimeUnit != "" || len(result.Warnings) == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseMassifRejectsMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"no snapshots": "time_unit: i\n",
		"bad snapshot": "snapshot=no\nmem_heap_B=1\n",
		"bad heap":     "snapshot=1\nmem_heap_B=nope\n",
		"bad timeunit": "time_unit: i\ntime_unit: ms\nsnapshot=1\nmem_heap_B=1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMassif(strings.NewReader(input)); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}
