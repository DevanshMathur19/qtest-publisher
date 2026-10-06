package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const junitFixture = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="sample" timestamp="2026-10-06T12:00:00Z">
    <testcase classname="io.example.CalculatorTest" name="adds[1]" time="0.5"/>
    <testcase classname="io.example.CalculatorTest" name="subtracts" time="0.2">
      <failure type="AssertionError" message="expected 2">stack trace</failure>
      <system-out>diagnostic output</system-out>
    </testcase>
    <testcase classname="io.example.SkippedTest" name="disabled" time="0">
      <skipped message="not supported"/>
    </testcase>
    <testsuite name="nested">
      <testcase name="errors" status="error" time="1.25">
        <system-err>connection failed</system-err>
      </testcase>
    </testsuite>
  </testsuite>
</testsuites>`

func writeJUnitFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.xml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseJUnitAndBuildClassResults(t *testing.T) {
	fallback := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	cases, err := ParseJUnitFile(writeJUnitFixture(t, junitFixture), fallback)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 4 {
		t.Fatalf("got %d cases", len(cases))
	}
	statuses := StatusMap{Passed: "PASSED", Failed: "FAILED", Error: "BROKEN", Skipped: "SKIPPED"}
	results := BuildResults(cases, "class", statuses)
	if len(results) != 3 {
		t.Fatalf("got %d grouped results", len(results))
	}
	var calculator TestResult
	for _, result := range results {
		if result.Identity == "io.example.CalculatorTest" {
			calculator = result
		}
	}
	if calculator.Status != "FAILED" || len(calculator.Steps) != 2 {
		t.Fatalf("unexpected grouped result: %#v", calculator)
	}
	if calculator.Steps[0].Order != 0 || calculator.Steps[1].Order != 1 {
		t.Fatalf("step order was not deterministic: %#v", calculator.Steps)
	}
	if !strings.Contains(calculator.Steps[1].ActualResult, "diagnostic output") {
		t.Fatalf("failure output missing: %#v", calculator.Steps[1])
	}
}

func TestBuildMethodResultsUsesStableMethodIdentity(t *testing.T) {
	cases, err := ParseJUnitFile(
		writeJUnitFixture(t, `<testsuite name="s"><testcase classname="C" name="m[2]" time="1"/></testsuite>`),
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	results := BuildResults(
		cases, "method",
		StatusMap{Passed: "P", Failed: "F", Error: "E", Skipped: "S"},
	)
	if len(results) != 1 || results[0].Identity != "C#m[2]" || results[0].Status != "P" {
		t.Fatalf("unexpected method result: %#v", results)
	}
	if !strings.Contains(automationIdentity(results[0].Identity), "C#m[2]") {
		t.Fatal("automation identity did not preserve parameterized method")
	}
}

func TestParseJUnitRejectsMalformedEmptyAndUnsupported(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":   `<testsuite><testcase></testsuite>`,
		"empty suite": `<testsuite name="empty"></testsuite>`,
		"unsupported": `<testng-results></testng-results>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseJUnitFile(writeJUnitFixture(t, content), time.Now()); err == nil {
				t.Fatal("expected parsing to fail")
			}
		})
	}
}

func TestJUnitRootSuiteAndTimestampFallback(t *testing.T) {
	fallback := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	cases, err := ParseJUnitFile(
		writeJUnitFixture(t, `<testsuite name="root"><testcase name="ok" time="bad"/></testsuite>`),
		fallback,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || !cases[0].Start.Equal(fallback) || !cases[0].End.Equal(fallback) {
		t.Fatalf("unexpected fallback time: %#v", cases)
	}
}
