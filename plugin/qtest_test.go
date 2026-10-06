package plugin

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func qtestConfig(server *httptest.Server) Config {
	return Config{
		BaseURL: server.URL, Token: "super-secret-token", ProjectID: 123,
		PollInterval: 5 * time.Millisecond, InsecureSkipTLS: true,
	}
}

func TestResolveSuiteReusesExactName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer super-secret-token" {
			t.Fatalf("authorization header missing")
		}
		if r.Method != http.MethodGet ||
			r.URL.Path != "/api/v3/projects/123/test-suites" ||
			r.URL.Query().Get("parentId") != "44" ||
			r.URL.Query().Get("parentType") != "release" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `[{"id":91,"name":"Daily"},{"id":92,"name":"Other"}]`)
	}))
	defer server.Close()
	client, err := NewQTestClient(qtestConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.ResolveSuite(context.Background(), "release", 44, "Daily", true)
	if err != nil || id != 91 {
		t.Fatalf("ResolveSuite = %d, %v", id, err)
	}
}

func TestResolveSuiteCreatesWhenMissing(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[]`)
		case http.MethodPost:
			posts.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "Daily" {
				t.Fatalf("unexpected suite body: %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":93,"name":"Daily"}`)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	client, _ := NewQTestClient(qtestConfig(server))
	id, err := client.ResolveSuite(context.Background(), "test-cycle", 55, "Daily", true)
	if err != nil || id != 93 || posts.Load() != 1 {
		t.Fatalf("ResolveSuite = %d, %v, posts=%d", id, err, posts.Load())
	}
}

func TestSubmitBatchPollsAllPendingStates(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/auto-test-logs"):
			var body automationRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.TestCycle != "456" || len(body.TestLogs) != 1 {
				t.Fatalf("unexpected submission: %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":77,"state":"IN_WAITING"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/projects/queue-processing/77":
			states := []string{"IN_WAITING", "IN_PROCESSING", "PENDING", "SUCCESS"}
			index := int(polls.Add(1)) - 1
			_, _ = io.WriteString(w, `{"id":77,"state":"`+states[index]+`"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	client, _ := NewQTestClient(qtestConfig(server))
	submission, err := client.SubmitBatch(
		context.Background(), "test-cycle", 456,
		[]automationLog{{Status: "PASSED", Name: "test", AutomationContent: "id"}},
		time.Now(),
	)
	if err != nil || submission.JobID != 77 || submission.State != "SUCCESS" {
		t.Fatalf("SubmitBatch = %#v, %v", submission, err)
	}
}

func TestPollRetriesThrottlingButSubmissionDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"id":8,"state":"SUCCESS"}`)
	}))
	defer server.Close()
	client, _ := NewQTestClient(qtestConfig(server))
	response, err := client.Poll(context.Background(), 8)
	if err != nil || response.State != "SUCCESS" || calls.Load() != 2 {
		t.Fatalf("Poll = %#v, %v, calls=%d", response, err, calls.Load())
	}

	calls.Store(0)
	_, err = client.SubmitBatch(
		context.Background(), "test-cycle", 1,
		[]automationLog{{Status: "PASSED", Name: "test", AutomationContent: "id"}},
		time.Now(),
	)
	var indeterminate *IndeterminateSubmissionError
	if !errors.As(err, &indeterminate) || calls.Load() != 1 {
		t.Fatalf("expected one indeterminate POST, got %v, calls=%d", err, calls.Load())
	}
}

func TestCustomCAAndCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":9,"state":"PENDING"}`)
	}))
	defer server.Close()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := qtestConfig(server)
	cfg.InsecureSkipTLS = false
	cfg.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	client, err := NewQTestClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Poll(ctx, 9)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestExplicitProxyIsUsed(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":10,"state":"SUCCESS"}`)
	}))
	defer target.Close()
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		downstream, _, err := hijacker.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = downstream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			defer downstream.Close()
			defer upstream.Close()
			_, _ = io.Copy(upstream, downstream)
		}()
		_, _ = io.Copy(downstream, upstream)
	}))
	defer proxy.Close()

	cfg := qtestConfig(target)
	cfg.ProxyURL = proxy.URL
	client, err := NewQTestClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Poll(context.Background(), 10)
	if err != nil || response.State != "SUCCESS" {
		t.Fatalf("Poll through proxy = %#v, %v", response, err)
	}
	if connects.Load() != 1 {
		t.Fatalf("expected one CONNECT, got %d", connects.Load())
	}
}

func TestAutomationIdentityEscapesXML(t *testing.T) {
	value := automationIdentity(`Class#"method<&`)
	if strings.Contains(value, "<&") || !strings.Contains(value, "&lt;&amp;") {
		t.Fatalf("identity was not escaped: %s", value)
	}
}
