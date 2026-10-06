package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunPublishesJUnitAndWritesOutputs(t *testing.T) {
	var submitted automationRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":101,"state":"IN_WAITING"}`)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":101,"state":"SUCCESS"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	resultDir := filepath.Join(root, "build", "junit")
	if err := os.MkdirAll(resultDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(resultDir, "TEST-results.xml"), []byte(junitFixture), 0600,
	); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "drone-output.env")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	cfg := qtestConfig(server)
	cfg.ResultPaths = []string{"**/junit/*.xml"}
	cfg.DestinationType = "test-cycle"
	cfg.DestinationID = 456
	cfg.IdentityMode = "method"
	cfg.Statuses = StatusMap{Passed: "PASSED", Failed: "FAILED", Error: "FAILED", Skipped: "SKIPPED"}
	cfg.ModuleNames = []string{"Automation", "Harness"}
	cfg.EmptyResult = "warn"
	cfg.BatchSize = 100
	cfg.MaxPayloadBytes = 1024 * 1024
	cfg.OutputPath = outputPath
	cfg.ExecutionDate = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

	var logs bytes.Buffer
	output, err := Run(context.Background(), cfg, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if output.MatchedFiles != 1 || output.ParsedTests != 4 ||
		output.SubmittedLogs != 4 || len(output.JobIDs) != 1 {
		t.Fatalf("unexpected outputs: %#v", output)
	}
	if len(submitted.TestLogs) != 4 || submitted.TestCycle != "456" {
		t.Fatalf("unexpected API payload: %#v", submitted)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"QTEST_MATCHED_FILES=1",
		"QTEST_PARSED_TESTS=4",
		"QTEST_JOB_IDS=101",
		"QTEST_STATE=SUCCESS",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("missing output %q in %s", expected, data)
		}
	}
	if strings.Contains(logs.String(), cfg.Token) {
		t.Fatal("token appeared in logs")
	}
}

func TestRunNoMatchWarnsWithoutCallingAPI(t *testing.T) {
	root := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	cfg := Config{
		ResultPaths: []string{"**/*.xml"}, EmptyResult: "warn",
		OutputPath: filepath.Join(root, "output.env"),
	}
	output, err := Run(context.Background(), cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if output.State != "SKIPPED" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestRunWritesPartialOutputsBeforeReturningBatchFailure(t *testing.T) {
	postCount := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":501,"state":"SUCCESS"}`)
			return
		}
		postCount++
		if postCount == 1 {
			_, _ = io.WriteString(w, `{"id":501,"state":"PENDING"}`)
			return
		}
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "results.xml"),
		[]byte(`<testsuite name="suite"><testcase classname="C" name="one"/><testcase classname="C" name="two"/></testsuite>`),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	outputPath := filepath.Join(root, "output.env")
	cfg := qtestConfig(server)
	cfg.ResultPaths = []string{"results.xml"}
	cfg.DestinationType = "test-cycle"
	cfg.DestinationID = 1
	cfg.IdentityMode = "method"
	cfg.Statuses = StatusMap{Passed: "PASSED", Failed: "FAILED", Error: "FAILED", Skipped: "SKIPPED"}
	cfg.ModuleNames = []string{"Automation"}
	cfg.BatchSize = 1
	cfg.MaxPayloadBytes = 1024 * 1024
	cfg.OutputPath = outputPath
	cfg.ExecutionDate = time.Now()

	output, err := Run(context.Background(), cfg, log.New(io.Discard, "", 0))
	if err == nil || output.State != "PARTIAL" || output.SubmittedLogs != 1 {
		t.Fatalf("expected one completed partial batch, output=%#v err=%v", output, err)
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "QTEST_STATE=PARTIAL") ||
		!strings.Contains(string(data), "QTEST_JOB_IDS=501") {
		t.Fatalf("partial reconciliation outputs missing: %s", data)
	}
}

func TestRunWritesIndeterminateOutputsForAmbiguousSuiteCreation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "results.xml"),
		[]byte(`<testsuite name="suite"><testcase classname="C" name="one"/></testsuite>`),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	outputPath := filepath.Join(root, "output.env")
	cfg := qtestConfig(server)
	cfg.ResultPaths = []string{"results.xml"}
	cfg.DestinationType = "release"
	cfg.DestinationID = 44
	cfg.SuiteName = "Harness"
	cfg.ReuseSuite = true
	cfg.IdentityMode = "method"
	cfg.Statuses = StatusMap{Passed: "PASSED", Failed: "FAILED", Error: "FAILED", Skipped: "SKIPPED"}
	cfg.ModuleNames = []string{"Automation"}
	cfg.OutputPath = outputPath
	cfg.ExecutionDate = time.Now()

	output, err := Run(context.Background(), cfg, log.New(io.Discard, "", 0))
	if err == nil || output.State != "INDETERMINATE" ||
		output.MatchedFiles != 1 || output.ParsedTests != 1 {
		t.Fatalf("expected indeterminate suite creation, output=%#v err=%v", output, err)
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "QTEST_STATE=INDETERMINATE") ||
		!strings.Contains(string(data), "QTEST_DESTINATION_ID=44") {
		t.Fatalf("indeterminate reconciliation outputs missing: %s", data)
	}
}

func TestSplitBatchesUsesCountAndEncodedBytes(t *testing.T) {
	logs := []automationLog{
		{Name: "one", Note: strings.Repeat("a", 100)},
		{Name: "two", Note: strings.Repeat("b", 100)},
		{Name: "three", Note: strings.Repeat("c", 100)},
	}
	executionDate := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	batches, err := splitBatches(logs, 2, 1000, "test-cycle", 1, executionDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 {
		t.Fatalf("unexpected count batches: %#v", batches)
	}
	oneSize, _ := encodedBatchSize(logs[:1], "test-cycle", 1, executionDate)
	batches, err = splitBatches(logs, 10, oneSize+10, "test-cycle", 1, executionDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatalf("expected byte-size batches, got %#v", batches)
	}
}
