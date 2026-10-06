package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Outputs struct {
	MatchedFiles  int
	ParsedTests   int
	SubmittedLogs int
	DestinationID int64
	JobIDs        []int64
	State         string
	ResultURL     string
}

func Run(ctx context.Context, cfg Config, logger *log.Logger) (Outputs, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return Outputs{}, fmt.Errorf("get workspace: %w", err)
	}
	files, err := MatchFiles(workdir, cfg.ResultPaths)
	if err != nil {
		return Outputs{}, err
	}
	if len(files) == 0 {
		if cfg.EmptyResult == "fail" {
			return Outputs{}, fmt.Errorf("no JUnit files matched %q", cfg.ResultPaths)
		}
		logger.Printf("No JUnit files matched; skipping qTest publication")
		output := Outputs{State: "SKIPPED"}
		if err := WriteOutputs(cfg.OutputPath, output); err != nil {
			return Outputs{}, err
		}
		return output, nil
	}

	var cases []flatCase
	for _, path := range files {
		parsed, err := ParseJUnitFile(path, cfg.ExecutionDate)
		if err != nil {
			return Outputs{}, err
		}
		cases = append(cases, parsed...)
		logger.Printf("Parsed %d JUnit case(s) from %s", len(parsed), safeRelative(workdir, path))
	}
	results := BuildResults(cases, cfg.IdentityMode, cfg.Statuses)
	if len(results) == 0 {
		return Outputs{}, errors.New("JUnit parsing produced no qTest results")
	}

	client, err := NewQTestClient(cfg)
	if err != nil {
		return Outputs{}, err
	}
	destinationType := cfg.DestinationType
	destinationID := cfg.DestinationID
	if destinationType == "release" {
		destinationID, err = client.ResolveSuite(
			ctx, "release", destinationID, cfg.SuiteName, cfg.ReuseSuite,
		)
		if err != nil {
			return Outputs{}, err
		}
		destinationType = "test-suite"
	} else if destinationType == "test-cycle" && cfg.SuiteName != "" {
		destinationID, err = client.ResolveSuite(
			ctx, "test-cycle", destinationID, cfg.SuiteName, cfg.ReuseSuite,
		)
		if err != nil {
			return Outputs{}, err
		}
		destinationType = "test-suite"
	}

	logs := make([]automationLog, 0, len(results))
	for _, result := range results {
		logs = append(logs, toAutomationLog(result, cfg))
	}
	output := Outputs{
		MatchedFiles:  len(files),
		ParsedTests:   len(cases),
		SubmittedLogs: len(logs),
		DestinationID: destinationID,
		State:         "SUCCESS",
		ResultURL:     resultURL(cfg.BaseURL, cfg.ProjectID, destinationType, destinationID),
	}

	if destinationType == "test-run" {
		for _, item := range logs {
			submission, err := client.SubmitToRun(ctx, destinationID, item)
			if err != nil {
				return Outputs{}, err
			}
			if submission.JobID > 0 {
				output.JobIDs = append(output.JobIDs, submission.JobID)
			}
		}
	} else {
		batches, err := splitBatches(
			logs, cfg.BatchSize, cfg.MaxPayloadBytes, destinationType, destinationID,
		)
		if err != nil {
			return Outputs{}, err
		}
		for index, batch := range batches {
			logger.Printf("Submitting qTest batch %d/%d with %d log(s)", index+1, len(batches), len(batch))
			submission, err := client.SubmitBatch(
				ctx, destinationType, destinationID, batch, cfg.ExecutionDate,
			)
			if err != nil {
				return Outputs{}, err
			}
			output.JobIDs = append(output.JobIDs, submission.JobID)
		}
	}
	if err := WriteOutputs(cfg.OutputPath, output); err != nil {
		return Outputs{}, err
	}
	logger.Printf(
		"Published %d qTest log(s) from %d JUnit case(s); destination=%s/%d",
		output.SubmittedLogs, output.ParsedTests, destinationType, destinationID,
	)
	return output, nil
}

func splitBatches(
	logs []automationLog, maxCount int, maxBytes int, destinationType string, destinationID int64,
) ([][]automationLog, error) {
	var batches [][]automationLog
	current := []automationLog{}
	for _, item := range logs {
		candidate := append(append([]automationLog(nil), current...), item)
		size, err := encodedBatchSize(candidate, destinationType, destinationID)
		if err != nil {
			return nil, err
		}
		if len(candidate) > maxCount || size > maxBytes {
			if len(current) == 0 {
				return nil, fmt.Errorf(
					"one qTest log exceeds PLUGIN_MAX_PAYLOAD_BYTES (%d > %d)", size, maxBytes,
				)
			}
			batches = append(batches, current)
			current = []automationLog{item}
			size, err = encodedBatchSize(current, destinationType, destinationID)
			if err != nil {
				return nil, err
			}
			if size > maxBytes {
				return nil, fmt.Errorf(
					"one qTest log exceeds PLUGIN_MAX_PAYLOAD_BYTES (%d > %d)", size, maxBytes,
				)
			}
		} else {
			current = candidate
		}
	}
	if len(current) != 0 {
		batches = append(batches, current)
	}
	return batches, nil
}

func encodedBatchSize(
	logs []automationLog, destinationType string, destinationID int64,
) (int, error) {
	request := automationRequest{TestLogs: logs}
	switch destinationType {
	case "test-cycle":
		request.TestCycle = strconv.FormatInt(destinationID, 10)
	case "test-suite":
		request.TestSuite = strconv.FormatInt(destinationID, 10)
	default:
		return 0, fmt.Errorf("cannot batch destination type %q", destinationType)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return 0, fmt.Errorf("encode qTest batch: %w", err)
	}
	return len(data), nil
}

func WriteOutputs(path string, output Outputs) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open DRONE_OUTPUT: %w", err)
	}
	defer file.Close()
	jobIDs := make([]string, 0, len(output.JobIDs))
	for _, id := range output.JobIDs {
		jobIDs = append(jobIDs, strconv.FormatInt(id, 10))
	}
	values := map[string]string{
		"QTEST_MATCHED_FILES":  strconv.Itoa(output.MatchedFiles),
		"QTEST_PARSED_TESTS":   strconv.Itoa(output.ParsedTests),
		"QTEST_SUBMITTED_LOGS": strconv.Itoa(output.SubmittedLogs),
		"QTEST_DESTINATION_ID": strconv.FormatInt(output.DestinationID, 10),
		"QTEST_JOB_IDS":        strings.Join(jobIDs, ","),
		"QTEST_STATE":          output.State,
		"QTEST_RESULT_URL":     output.ResultURL,
	}
	order := []string{
		"QTEST_MATCHED_FILES", "QTEST_PARSED_TESTS", "QTEST_SUBMITTED_LOGS",
		"QTEST_DESTINATION_ID", "QTEST_JOB_IDS", "QTEST_STATE", "QTEST_RESULT_URL",
	}
	for _, key := range order {
		value := strings.NewReplacer("\r", "", "\n", "").Replace(values[key])
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return fmt.Errorf("write DRONE_OUTPUT: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync DRONE_OUTPUT: %w", err)
	}
	return nil
}

func resultURL(baseURL string, projectID int64, destinationType string, destinationID int64) string {
	objectType := "3"
	switch destinationType {
	case "test-run":
		objectType = "4"
	case "test-cycle":
		objectType = "1"
	}
	return fmt.Sprintf(
		"%s/p/%d/portal/project#tab=testexecution&object=%s&id=%d",
		strings.TrimRight(baseURL, "/"), projectID, objectType, destinationID,
	)
}

func safeRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return filepath.Base(path)
	}
	return relative
}
