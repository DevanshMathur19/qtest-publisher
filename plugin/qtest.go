package plugin

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxErrorBodyBytes = 16 * 1024

type QTestClient struct {
	baseURL      string
	token        string
	projectID    int64
	http         *http.Client
	pollInterval time.Duration
}

type automationRequest struct {
	TestSuite     string          `json:"test_suite,omitempty"`
	TestCycle     string          `json:"test_cycle,omitempty"`
	ExecutionDate string          `json:"execution_date,omitempty"`
	TestLogs      []automationLog `json:"test_logs"`
}

type automationLog struct {
	Status            string              `json:"status"`
	ExecutionStart    string              `json:"exe_start_date"`
	ExecutionEnd      string              `json:"exe_end_date"`
	Name              string              `json:"name"`
	AutomationContent string              `json:"automation_content"`
	Note              string              `json:"note,omitempty"`
	BuildNumber       string              `json:"build_number,omitempty"`
	BuildURL          string              `json:"build_url,omitempty"`
	ModuleNames       []string            `json:"module_names,omitempty"`
	Properties        []Property          `json:"properties,omitempty"`
	TestStepLogs      []automationStepLog `json:"test_step_logs,omitempty"`
}

type automationStepLog struct {
	Description    string `json:"description"`
	ExpectedResult string `json:"expected_result"`
	ActualResult   string `json:"actual_result,omitempty"`
	Status         string `json:"status"`
	Order          int    `json:"order"`
	ExecutionDate  string `json:"exe_date,omitempty"`
}

type queueResponse struct {
	ID          int64  `json:"id"`
	State       string `json:"state"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Type        string `json:"type"`
}

type testSuiteResource struct {
	ID   int64  `json:"id"`
	PID  string `json:"pid"`
	Name string `json:"name"`
}

type Submission struct {
	JobID         int64
	State         string
	Content       string
	DestinationID int64
}

func NewQTestClient(cfg Config) (*QTestClient, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ResponseHeaderTimeout = 60 * time.Second
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipTLS, //nolint:gosec // explicit opt-in compatibility setting
	}
	if cfg.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("append custom CA certificate")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	if cfg.ProxyURL != "" {
		proxyURL, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &QTestClient{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		token:        cfg.Token,
		projectID:    cfg.ProjectID,
		http:         &http.Client{Transport: transport},
		pollInterval: cfg.PollInterval,
	}, nil
}

func (c *QTestClient) ResolveSuite(
	ctx context.Context, parentType string, parentID int64, name string, reuse bool,
) (int64, error) {
	if reuse {
		endpoint := fmt.Sprintf(
			"%s/api/v3/projects/%d/test-suites?parentId=%d&parentType=%s",
			c.baseURL, c.projectID, parentID, url.QueryEscape(parentType),
		)
		var suites []testSuiteResource
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &suites, true); err != nil {
			return 0, fmt.Errorf("list qTest suites: %w", err)
		}
		for _, suite := range suites {
			if suite.Name == name {
				if suite.ID <= 0 {
					return 0, errors.New("qTest returned a suite without a valid ID")
				}
				return suite.ID, nil
			}
		}
	}

	endpoint := fmt.Sprintf(
		"%s/api/v3/projects/%d/test-suites?parentId=%d&parentType=%s",
		c.baseURL, c.projectID, parentID, url.QueryEscape(parentType),
	)
	var suite testSuiteResource
	if err := c.doJSON(
		ctx, http.MethodPost, endpoint, map[string]any{"name": name}, &suite, false,
	); err != nil {
		return 0, fmt.Errorf("create qTest suite: %w", err)
	}
	if suite.ID <= 0 {
		return 0, errors.New("qTest created a suite without returning a valid ID")
	}
	return suite.ID, nil
}

func (c *QTestClient) SubmitBatch(
	ctx context.Context,
	destinationType string,
	destinationID int64,
	logs []automationLog,
	executionDate time.Time,
) (Submission, error) {
	if len(logs) == 0 {
		return Submission{}, errors.New("cannot submit an empty qTest batch")
	}
	request := automationRequest{
		ExecutionDate: executionDate.UTC().Format(time.RFC3339),
		TestLogs:      logs,
	}
	var endpoint string
	switch destinationType {
	case "test-cycle":
		request.TestCycle = strconv.FormatInt(destinationID, 10)
		endpoint = fmt.Sprintf(
			"%s/api/v3/projects/%d/auto-test-logs?type=automation",
			c.baseURL, c.projectID,
		)
	case "test-suite":
		request.TestSuite = strconv.FormatInt(destinationID, 10)
		endpoint = fmt.Sprintf(
			"%s/api/v3.1/projects/%d/test-runs/0/auto-test-logs?type=automation",
			c.baseURL, c.projectID,
		)
	default:
		return Submission{}, fmt.Errorf("batch submission does not support %q", destinationType)
	}
	var response queueResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint, request, &response, false); err != nil {
		return Submission{}, &IndeterminateSubmissionError{Err: err}
	}
	if response.ID <= 0 {
		return Submission{}, errors.New("qTest submission did not return a queue job ID")
	}
	response, err := c.Poll(ctx, response.ID)
	if err != nil {
		return Submission{}, err
	}
	return Submission{
		JobID: response.ID, State: response.State, Content: response.Content,
		DestinationID: destinationID,
	}, nil
}

func (c *QTestClient) SubmitToRun(
	ctx context.Context, testRunID int64, log automationLog,
) (Submission, error) {
	endpoint := fmt.Sprintf(
		"%s/api/v3/projects/%d/test-runs/%d/auto-test-logs",
		c.baseURL, c.projectID, testRunID,
	)
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodPost, endpoint, log, &response, false); err != nil {
		return Submission{}, &IndeterminateSubmissionError{Err: err}
	}
	return Submission{State: "SUCCESS", DestinationID: testRunID}, nil
}

func (c *QTestClient) Poll(ctx context.Context, jobID int64) (queueResponse, error) {
	endpoint := fmt.Sprintf("%s/api/v3/projects/queue-processing/%d", c.baseURL, jobID)
	timer := time.NewTicker(c.pollInterval)
	defer timer.Stop()
	for {
		var response queueResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response, true); err != nil {
			return queueResponse{}, fmt.Errorf("poll qTest queue job %d: %w", jobID, err)
		}
		if response.ID == 0 {
			response.ID = jobID
		}
		switch strings.ToUpper(response.State) {
		case "SUCCESS":
			return response, nil
		case "FAILED":
			return queueResponse{}, fmt.Errorf(
				"qTest queue job %d failed: %s", jobID, limitText(response.Content),
			)
		case "IN_WAITING", "IN_PROCESSING", "PENDING", "":
		default:
			return queueResponse{}, fmt.Errorf(
				"qTest queue job %d returned unknown state %q", jobID, response.State,
			)
		}
		select {
		case <-ctx.Done():
			return queueResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
}

type IndeterminateSubmissionError struct {
	Err error
}

func (e *IndeterminateSubmissionError) Error() string {
	return "qTest submission outcome is indeterminate; reconcile in qTest before retrying: " + e.Err.Error()
}

func (e *IndeterminateSubmissionError) Unwrap() error {
	return e.Err
}

func (c *QTestClient) doJSON(
	ctx context.Context,
	method string,
	endpoint string,
	body any,
	output any,
	retrySafe bool,
) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	attempts := 1
	if retrySafe {
		attempts = 4
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.token)
		if payload != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.http.Do(request)
		if err != nil {
			if retrySafe && attempt < attempts && ctx.Err() == nil {
				if err := waitRetry(ctx, time.Duration(attempt)*200*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("send request: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close response: %w", closeErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if retrySafe && attempt < attempts && isRetryableStatus(response.StatusCode) {
				delay := retryDelay(response, attempt)
				if err := waitRetry(ctx, delay); err != nil {
					return err
				}
				continue
			}
			bodyText := limitText(string(data))
			if c.token != "" {
				bodyText = strings.ReplaceAll(bodyText, c.token, "[REDACTED]")
			}
			return fmt.Errorf("qTest API returned HTTP %d: %s", response.StatusCode, bodyText)
		}
		if output != nil && len(bytes.TrimSpace(data)) != 0 {
			if err := json.Unmarshal(data, output); err != nil {
				return fmt.Errorf("decode qTest response: %w", err)
			}
		}
		return nil
	}
	return errors.New("request attempts exhausted")
}

func toAutomationLog(result TestResult, cfg Config) automationLog {
	steps := make([]automationStepLog, 0, len(result.Steps))
	for _, step := range result.Steps {
		steps = append(steps, automationStepLog{
			Description:    step.Description,
			ExpectedResult: step.ExpectedResult,
			ActualResult:   step.ActualResult,
			Status:         step.Status,
			Order:          step.Order,
			ExecutionDate:  step.ExecutionDate.UTC().Format(time.RFC3339Nano),
		})
	}
	return automationLog{
		Status:            result.Status,
		ExecutionStart:    result.Start.UTC().Format(time.RFC3339Nano),
		ExecutionEnd:      result.End.UTC().Format(time.RFC3339Nano),
		Name:              result.Name,
		AutomationContent: automationIdentity(result.Identity),
		Note:              result.Note,
		BuildNumber:       cfg.BuildNumber,
		BuildURL:          cfg.BuildURL,
		ModuleNames:       append([]string(nil), cfg.ModuleNames...),
		Properties:        append([]Property(nil), cfg.Properties...),
		TestStepLogs:      steps,
	}
}

func automationIdentity(identity string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(identity))
	return `<testcase identity="` + escaped.String() + `"/>`
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func retryDelay(response *http.Response, attempt int) time.Duration {
	if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 && seconds <= 60 {
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Duration(attempt) * 200 * time.Millisecond
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
