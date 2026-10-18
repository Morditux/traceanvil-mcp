package ctest

import (
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func TestParseJUnitCTestFormat(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="(empty)" tests="4" failures="1" disabled="1" skipped="0">
  <testcase name="pass" classname="pass" time="0.01" status="run">
    <system-out>pass
</system-out>
  </testcase>
  <testcase name="fail" classname="fail" time="0.02" status="fail">
    <failure message="Failed"/>
  </testcase>
  <testcase name="disabled" classname="disabled" time="0" status="disabled"/>
  <testcase name="timeout" classname="timeout" time="1" status="fail">
    <failure message="Timeout"/>
  </testcase>
</testsuite>`
	issues, _, err := parseJUnit([]byte(xmlData))
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 4 {
		t.Fatalf("issues = %+v", issues)
	}
	statuses := map[string]TestStatus{}
	for _, issue := range issues {
		statuses[issue.Name] = issue.Status
	}
	if statuses["pass"] != TestPassed || statuses["fail"] != TestFailed ||
		statuses["disabled"] != TestSkipped || statuses["timeout"] != TestTimeout {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestParseJUnitRejectsInvalidDocuments(t *testing.T) {
	for _, data := range []string{"", "<testsuites></testsuites>", "<other/>", "<testsuite><testcase name=\"\"/></testsuite>"} {
		if _, _, err := parseJUnit([]byte(data)); err == nil {
			t.Errorf("parseJUnit(%q) accepted invalid document", data)
		}
	}
}

func TestExactTestSelectorEscapesNames(t *testing.T) {
	selector, err := exactTestSelector([]string{"a+b", "x(y)"})
	if err != nil {
		t.Fatal(err)
	}
	if selector != `^(a\+b|x\(y\))$` {
		t.Errorf("selector = %q", selector)
	}
}

func TestParseDiscoveryFiltersAllowedTests(t *testing.T) {
	project := configProjectForTest()
	discovery, err := parseDiscovery([]byte(`{"tests":[{"name":"pass"},{"name":"other"},{"name":" bad"},{"name":"bad\u0001"}]}`), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Tests) != 1 || discovery.Tests[0] != "pass" {
		t.Errorf("discovery = %+v", discovery)
	}
	if !discovery.Complete || len(discovery.Missing) != 0 {
		t.Errorf("discovery completeness = %+v", discovery)
	}
}

func configProjectForTest() config.CTestProject {
	return config.CTestProject{ID: "tests", AllowedTests: []string{"pass"}}
}

func TestFailureOutputMentioningTimeoutIsNotACTestTimeout(t *testing.T) {
	xmlData := `<testsuite><testcase name="fail" time="0.1" status="fail">` +
		`<failure message="Failed">connection timeout after 30s</failure></testcase></testsuite>`
	issues, _, err := parseJUnit([]byte(xmlData))
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Status != TestFailed {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestExactTestSelectorRejectsOversizedSelection(t *testing.T) {
	tests := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		tests = append(tests, strings.Repeat("x", 300))
	}
	if _, err := exactTestSelector(tests); err == nil {
		t.Fatal("oversized selector was accepted")
	}
}

func TestParseJUnitSecondsRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-Inf", "1e999", "-1"} {
		if got := parseJUnitSeconds(value); got != 0 {
			t.Errorf("parseJUnitSeconds(%q) = %d, want 0", value, got)
		}
	}
}
