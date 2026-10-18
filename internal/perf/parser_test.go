package perf

import (
	"strings"
	"testing"
)

func TestParseStatJSON(t *testing.T) {
	input := `{"event":"task-clock","counter-value":"12.5","unit":"msec","pcnt-running":"50"}
{"event":"cycles","counter-value":"1000"}
{"event":"instructions","counter-value":"750","unit":"count"}
`
	result, err := ParseStat(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 3 {
		t.Fatalf("events = %+v", result.Events)
	}
	if result.Events[0].Name != "task-clock" || result.Events[0].Value != 12.5 ||
		result.Events[0].Unit != "msec" || result.Events[0].RunningPercent != 50 {
		t.Fatalf("task clock = %+v", result.Events[0])
	}
	if result.Events[1].Value != 1000 || result.Events[2].Name != "instructions" {
		t.Fatalf("events = %+v", result.Events)
	}
}

func TestParseStatMarksUnsupportedAndRejectsEmpty(t *testing.T) {
	result, err := ParseStat(strings.NewReader(`{"event":"cycles","counter-value":"not counted"}`))
	if err == nil {
		t.Fatal("expected no numeric event error")
	}
	if result != nil {
		t.Fatalf("result = %+v", result)
	}

	result, err = ParseStat(strings.NewReader(`{"event":"cycles","counter-value":"not counted"}
{"event":"task-clock","counter-value":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseStatRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseStat(strings.NewReader(`{"event":"cycles","counter-value":`)); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}
