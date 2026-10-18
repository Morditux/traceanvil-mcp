package ctest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	maxJUnitTests    = 1024
	maxJUnitMessage  = 4096
	maxJUnitClassLen = 512
)

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type junitError struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type junitSkipped struct{}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Status    string        `xml:"status,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure"`
	Error     *junitError   `xml:"error"`
	Skipped   *junitSkipped `xml:"skipped"`
}

// parseJUnit accepts either a <testsuite> root (as emitted by CTest) or a
// <testsuites> wrapper. It decodes one testcase at a time so a maliciously wide
// document cannot build all cases before the count bound is checked.
func parseJUnit(data []byte) ([]junitIssue, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, fmt.Errorf("empty JUnit document")
	}
	return parseJUnitReader(bytes.NewReader(data))
}

func parseJUnitReader(reader io.Reader) ([]junitIssue, []string, error) {
	decoder := xml.NewDecoder(reader)
	issues := make([]junitIssue, 0)
	warnings := make([]string, 0)
	rootSeen := false
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, warnings, fmt.Errorf("read JUnit XML: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if !rootSeen {
				rootSeen = true
				switch element.Name.Local {
				case "testsuite", "testsuites":
				default:
					return nil, warnings, fmt.Errorf("unexpected JUnit root %q", element.Name.Local)
				}
				continue
			}
			if element.Name.Local != "testcase" {
				continue
			}
			if len(issues) >= maxJUnitTests {
				return nil, warnings, fmt.Errorf("JUnit contains more than %d testcases", maxJUnitTests)
			}
			var test junitCase
			if err := decoder.DecodeElement(&test, &element); err != nil {
				return nil, warnings, fmt.Errorf("parse JUnit testcase: %w", err)
			}
			depth--
			name := test.Name
			if name == "" || name != strings.TrimSpace(name) || len(name) > maxJUnitClassLen {
				return nil, warnings, fmt.Errorf("JUnit testcase has an invalid name")
			}
			status, message := classifyJUnitCase(test)
			issues = append(issues, junitIssue{
				Name: name, Status: status, DurationMS: parseJUnitSeconds(test.Time),
				Message: truncateText(message, maxJUnitMessage),
			})
		case xml.EndElement:
			depth--
		}
	}
	if !rootSeen {
		return nil, warnings, fmt.Errorf("JUnit has no root element")
	}
	if len(issues) == 0 {
		return nil, warnings, fmt.Errorf("JUnit contains no testcases")
	}
	return issues, warnings, nil
}

func classifyJUnitCase(test junitCase) (TestStatus, string) {
	switch strings.ToLower(strings.TrimSpace(test.Status)) {
	case "disabled", "notrun", "not_run":
		return TestSkipped, "test was not run"
	}
	if test.Skipped != nil {
		return TestSkipped, "test was skipped"
	}
	if test.Error != nil {
		message := test.Error.Message
		if message == "" {
			message = strings.TrimSpace(test.Error.Text)
		}
		if message == "" {
			message = "CTest reported an error"
		}
		return TestFailed, message
	}
	if test.Failure != nil {
		message := test.Failure.Message
		if message == "" {
			message = strings.TrimSpace(test.Failure.Text)
		}
		if message == "" {
			message = "CTest reported a failure"
		}
		if strings.EqualFold(strings.TrimSpace(message), "timeout") {
			return TestTimeout, message
		}
		return TestFailed, message
	}
	return TestPassed, ""
}

func parseJUnitSeconds(value string) int64 {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) ||
		seconds < 0 || seconds > 24*60*60 {
		return 0
	}
	return int64(seconds * 1000)
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
