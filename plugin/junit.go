package plugin

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxJUnitFileBytes = 32 * 1024 * 1024
	maxLogTextBytes   = 64 * 1024
	maxCasesPerFile   = 1_000_000
	maxSuiteDepth     = 256
)

type StepResult struct {
	Description    string
	ExpectedResult string
	ActualResult   string
	Status         string
	Order          int
	ExecutionDate  time.Time
}

type TestResult struct {
	Name       string
	Identity   string
	Status     string
	Start      time.Time
	End        time.Time
	Note       string
	Steps      []StepResult
	SourceFile string
}

type junitCase struct {
	Name       string          `xml:"name,attr"`
	ClassName  string          `xml:"classname,attr"`
	Time       string          `xml:"time,attr"`
	Timestamp  string          `xml:"timestamp,attr"`
	Status     string          `xml:"status,attr"`
	Failure    *junitDetail    `xml:"failure"`
	Error      *junitDetail    `xml:"error"`
	Skipped    *junitDetail    `xml:"skipped"`
	SystemOut  string          `xml:"system-out"`
	SystemErr  string          `xml:"system-err"`
	Properties []junitProperty `xml:"properties>property"`
}

type junitDetail struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type flatCase struct {
	Suite      string
	Name       string
	ClassName  string
	Status     string
	Start      time.Time
	End        time.Time
	Detail     string
	SystemOut  string
	SystemErr  string
	Properties []junitProperty
	SourceFile string
}

func ParseJUnitFile(path string, fallback time.Time) ([]flatCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open JUnit file %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat JUnit file %s: %w", path, err)
	}
	if info.Size() > maxJUnitFileBytes {
		return nil, fmt.Errorf("JUnit file %s exceeds %d bytes", path, maxJUnitFileBytes)
	}

	decoder := xml.NewDecoder(io.LimitReader(file, maxJUnitFileBytes+1))
	var root xml.StartElement
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("JUnit file %s is empty", path)
			}
			return nil, fmt.Errorf("decode JUnit file %s: %w", path, err)
		}
		if start, ok := token.(xml.StartElement); ok {
			root = start
			break
		}
	}

	if root.Name.Local != "testsuite" && root.Name.Local != "testsuites" {
		return nil, fmt.Errorf("unsupported JUnit root element %q in %s", root.Name.Local, path)
	}

	var result []flatCase
	type suiteContext struct {
		name  string
		start time.Time
	}
	stack := []suiteContext{}
	if root.Name.Local == "testsuite" {
		stack = append(stack, suiteContext{
			name:  attribute(root, "name"),
			start: parseJUnitTime(attribute(root, "timestamp"), fallback),
		})
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode JUnit file %s: %w", path, err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "testsuite":
				if len(stack) >= maxSuiteDepth {
					return nil, fmt.Errorf("JUnit file %s exceeds suite depth %d", path, maxSuiteDepth)
				}
				parentName := ""
				parentStart := fallback
				if len(stack) != 0 {
					parentName = stack[len(stack)-1].name
					parentStart = stack[len(stack)-1].start
				}
				name := strings.TrimSpace(attribute(value, "name"))
				if parentName != "" {
					if name == "" {
						name = parentName
					} else {
						name = parentName + "/" + name
					}
				}
				stack = append(stack, suiteContext{
					name:  name,
					start: parseJUnitTime(attribute(value, "timestamp"), parentStart),
				})
			case "testcase":
				if len(result) >= maxCasesPerFile {
					return nil, fmt.Errorf("JUnit file %s exceeds %d test cases", path, maxCasesPerFile)
				}
				var test junitCase
				if err := decoder.DecodeElement(&test, &value); err != nil {
					return nil, fmt.Errorf("decode JUnit testcase %s: %w", path, err)
				}
				suiteName := ""
				suiteStart := fallback
				if len(stack) != 0 {
					suiteName = stack[len(stack)-1].name
					suiteStart = stack[len(stack)-1].start
				}
				start := parseJUnitTime(test.Timestamp, suiteStart)
				status, detail := caseStatus(test)
				result = append(result, flatCase{
					Suite:      suiteName,
					Name:       strings.TrimSpace(test.Name),
					ClassName:  strings.TrimSpace(test.ClassName),
					Status:     status,
					Start:      start,
					End:        start.Add(parseDurationSeconds(test.Time)),
					Detail:     limitText(detail),
					SystemOut:  limitText(test.SystemOut),
					SystemErr:  limitText(test.SystemErr),
					Properties: test.Properties,
					SourceFile: path,
				})
			}
		case xml.EndElement:
			if value.Name.Local == "testsuite" && len(stack) != 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("JUnit file %s contains no test cases", path)
	}
	return result, nil
}

func attribute(element xml.StartElement, name string) string {
	for _, item := range element.Attr {
		if item.Name.Local == name {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func BuildResults(cases []flatCase, identityMode string, statuses StatusMap) []TestResult {
	if identityMode == "method" {
		results := make([]TestResult, 0, len(cases))
		for _, test := range cases {
			className := classIdentity(test)
			name := test.Name
			if name == "" {
				name = className
			}
			results = append(results, TestResult{
				Name:       name,
				Identity:   className + "#" + name,
				Status:     mapStatus(test.Status, statuses),
				Start:      test.Start,
				End:        test.End,
				Note:       caseNote(test),
				SourceFile: test.SourceFile,
			})
		}
		sortResults(results)
		return results
	}

	groups := map[string][]flatCase{}
	for _, test := range cases {
		identity := classIdentity(test)
		groups[identity] = append(groups[identity], test)
	}
	results := make([]TestResult, 0, len(groups))
	for identity, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Name == group[j].Name {
				return group[i].SourceFile < group[j].SourceFile
			}
			return group[i].Name < group[j].Name
		})
		result := TestResult{
			Name:       identity,
			Identity:   identity,
			Status:     statuses.Skipped,
			Start:      group[0].Start,
			End:        group[0].End,
			SourceFile: group[0].SourceFile,
		}
		allSkipped := true
		for index, test := range group {
			if test.Start.Before(result.Start) {
				result.Start = test.Start
			}
			if test.End.After(result.End) {
				result.End = test.End
			}
			if test.Status != "skipped" {
				allSkipped = false
			}
			result.Status = strongerStatus(result.Status, mapStatus(test.Status, statuses), statuses)
			result.Steps = append(result.Steps, StepResult{
				Description:    nonEmpty(test.Name, "unnamed test"),
				ExpectedResult: "test completes successfully",
				ActualResult:   caseNote(test),
				Status:         mapStatus(test.Status, statuses),
				Order:          index,
				ExecutionDate:  test.End,
			})
		}
		if allSkipped {
			result.Status = statuses.Skipped
		}
		result.Note = fmt.Sprintf("%d JUnit test method(s) from %s", len(group), result.SourceFile)
		results = append(results, result)
	}
	sortResults(results)
	return results
}

func classIdentity(test flatCase) string {
	if value := strings.TrimSpace(test.ClassName); value != "" {
		return value
	}
	if value := strings.TrimSpace(test.Suite); value != "" {
		return value
	}
	return "unnamed-test-class"
}

func caseStatus(test junitCase) (string, string) {
	if test.Error != nil {
		return "error", detailText(test.Error)
	}
	if test.Failure != nil {
		return "failed", detailText(test.Failure)
	}
	if test.Skipped != nil {
		return "skipped", detailText(test.Skipped)
	}
	switch strings.ToLower(strings.TrimSpace(test.Status)) {
	case "error":
		return "error", ""
	case "failed", "failure":
		return "failed", ""
	case "skipped", "disabled", "notrun", "not run", "ignored":
		return "skipped", ""
	default:
		return "passed", ""
	}
}

func detailText(detail *junitDetail) string {
	if detail == nil {
		return ""
	}
	parts := []string{}
	if detail.Type != "" {
		parts = append(parts, detail.Type)
	}
	if detail.Message != "" {
		parts = append(parts, detail.Message)
	}
	if body := strings.TrimSpace(detail.Body); body != "" {
		parts = append(parts, body)
	}
	return strings.Join(parts, ": ")
}

func caseNote(test flatCase) string {
	parts := []string{}
	if test.Detail != "" {
		parts = append(parts, test.Detail)
	}
	if test.SystemOut != "" {
		parts = append(parts, "stdout:\n"+test.SystemOut)
	}
	if test.SystemErr != "" {
		parts = append(parts, "stderr:\n"+test.SystemErr)
	}
	for _, property := range test.Properties {
		if property.Name != "" {
			parts = append(parts, property.Name+"="+property.Value)
		}
	}
	if len(parts) == 0 {
		return "JUnit result: " + test.Status
	}
	return limitText(strings.Join(parts, "\n"))
}

func parseJUnitTime(value string, fallback time.Time) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return fallback.UTC()
}

func parseDurationSeconds(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func mapStatus(status string, statuses StatusMap) string {
	switch status {
	case "failed":
		return statuses.Failed
	case "error":
		return statuses.Error
	case "skipped":
		return statuses.Skipped
	default:
		return statuses.Passed
	}
}

func strongerStatus(current, candidate string, statuses StatusMap) string {
	rank := func(value string) int {
		switch value {
		case statuses.Error:
			return 4
		case statuses.Failed:
			return 3
		case statuses.Passed:
			return 2
		case statuses.Skipped:
			return 1
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func sortResults(results []TestResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Identity == results[j].Identity {
			return results[i].Name < results[j].Name
		}
		return results[i].Identity < results[j].Identity
	})
}

func limitText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLogTextBytes {
		return value
	}
	return value[:maxLogTextBytes] + "\n[truncated]"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
