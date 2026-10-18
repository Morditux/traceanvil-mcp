package analysis

import (
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

func TestParseFilterValidation(t *testing.T) {
	tests := []struct {
		name    string
		request ReportRequest
		wantErr bool
	}{
		{name: "empty", request: ReportRequest{}},
		{name: "kinds", request: ReportRequest{Kinds: []string{"InvalidRead", "InvalidWrite"}}},
		{name: "too many kinds", request: ReportRequest{Kinds: make([]string, maxFilterKinds+1)}, wantErr: true},
		{name: "empty kind", request: ReportRequest{Kinds: []string{""}}, wantErr: true},
		{name: "nul kind", request: ReportRequest{Kinds: []string{"bad\x00kind"}}, wantErr: true},
		{name: "invalid severity", request: ReportRequest{Severities: []string{"fatal"}}, wantErr: true},
		{name: "too many severities", request: ReportRequest{Severities: []string{"error", "warning", "info", "error"}}, wantErr: true},
		{name: "path prefix", request: ReportRequest{PathPrefix: "src/"}},
		{name: "nul path prefix", request: ReportRequest{PathPrefix: "src/\x00"}, wantErr: true},
		{name: "fingerprint", request: ReportRequest{Fingerprint: "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		{name: "bad fingerprint", request: ReportRequest{Fingerprint: "sha256:abc"}, wantErr: true},
		{name: "negative occurrences", request: ReportRequest{MinOccurrences: -1}, wantErr: true},
		{name: "huge occurrences", request: ReportRequest{MinOccurrences: maxFilterOccurrence + 1}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseFilter(test.request)
			if test.wantErr {
				mustCode(t, err, CodeInvalidArgument)
				return
			}
			if err != nil {
				t.Fatalf("parseFilter() error = %v", err)
			}
		})
	}
}

func TestFilterMatches(t *testing.T) {
	item := diagnostic.Diagnostic{
		Kind:        "InvalidRead",
		Severity:    diagnostic.SeverityError,
		Fingerprint: "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Occurrences: 3,
		Stack: []diagnostic.Frame{
			{Function: "main", Path: "src/main.c", Line: 10},
			{Function: "helper", Path: "src/helper.c", Line: 20},
		},
		ContextStacks: []diagnostic.ContextStack{{
			Label:  "allocation",
			Frames: []diagnostic.Frame{{Function: "alloc", Path: "lib/alloc.c", Line: 5}},
		}},
		OriginStack: []diagnostic.Frame{{Function: "origin", Path: "src/origin.c", Line: 1}},
	}

	tests := []struct {
		name   string
		filter filter
		want   bool
	}{
		{name: "empty", filter: filter{}, want: true},
		{name: "kind", filter: filter{kinds: []string{"InvalidRead"}}, want: true},
		{name: "other kind", filter: filter{kinds: []string{"InvalidWrite"}}, want: false},
		{name: "severity", filter: filter{severities: []diagnostic.Severity{diagnostic.SeverityError}}, want: true},
		{name: "other severity", filter: filter{severities: []diagnostic.Severity{diagnostic.SeverityInfo}}, want: false},
		{name: "fingerprint", filter: filter{fingerprint: item.Fingerprint}, want: true},
		{name: "other fingerprint", filter: filter{fingerprint: "sha256:" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}, want: false},
		{name: "min occurrences", filter: filter{minOccurrences: 3}, want: true},
		{name: "too many occurrences", filter: filter{minOccurrences: 4}, want: false},
		{name: "path prefix", filter: filter{pathPrefix: "src/"}, want: true},
		{name: "context path prefix", filter: filter{pathPrefix: "lib/"}, want: true},
		{name: "origin path prefix", filter: filter{pathPrefix: "src/origin"}, want: true},
		{name: "unknown path", filter: filter{pathPrefix: "vendor/"}, want: false},
		{name: "function", filter: filter{function: "helper"}, want: true},
		{name: "context function", filter: filter{function: "alloc"}, want: true},
		{name: "unknown function", filter: filter{function: "absent"}, want: false},
		{name: "path and function same frame", filter: filter{pathPrefix: "src/", function: "helper"}, want: true},
		{name: "path and function different frames", filter: filter{pathPrefix: "src/", function: "alloc"}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.filter.matches(item); got != test.want {
				t.Errorf("matches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFilterHashIsOrderInsensitiveAndIncludesAllFields(t *testing.T) {
	first, err := parseFilter(ReportRequest{
		Kinds:      []string{"InvalidRead", "InvalidWrite"},
		Severities: []string{"error", "warning"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseFilter(ReportRequest{
		Kinds:      []string{"InvalidWrite", "InvalidRead"},
		Severities: []string{"warning", "error"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.hash() != second.hash() {
		t.Error("filter hash depends on input order")
	}

	variants := []ReportRequest{
		{Kinds: []string{"InvalidRead"}},
		{PathPrefix: "src/"},
		{Function: "main"},
		{Fingerprint: "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{MinOccurrences: 2},
	}
	for _, request := range variants {
		filter, err := parseFilter(request)
		if err != nil {
			t.Fatal(err)
		}
		if filter.hash() == first.hash() {
			t.Errorf("filter %+v has the same hash as another filter", request)
		}
	}
}
