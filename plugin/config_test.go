package plugin

import (
	"os"
	"strings"
	"testing"
	"time"
)

func setMinimumEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PLUGIN_QTEST_URL", "PLUGIN_URL", "PLUGIN_BEARER_TOKEN", "PLUGIN_API_TOKEN",
		"PLUGIN_TOKEN", "PLUGIN_PROJECT_ID", "PLUGIN_DESTINATION_TYPE",
		"PLUGIN_DESTINATION_ID", "PLUGIN_RELEASE_ID", "PLUGIN_TEST_CYCLE_ID",
		"PLUGIN_TEST_SUITE_ID", "PLUGIN_TEST_RUN_ID", "PLUGIN_SUITE_NAME",
		"PLUGIN_RESULT_PATH", "PLUGIN_RESULT_PATHS", "PLUGIN_PROPERTIES",
		"PLUGIN_ENVIRONMENT", "PLUGIN_ENVIRONMENT_FIELD_ID", "PLUGIN_PROXY_URL",
		"HARNESS_HTTPS_PROXY", "HTTPS_PROXY", "PLUGIN_CA_CERT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("PLUGIN_QTEST_URL", "https://example.qtestnet.com")
	t.Setenv("PLUGIN_BEARER_TOKEN", "secret-value")
	t.Setenv("PLUGIN_PROJECT_ID", "123")
	t.Setenv("PLUGIN_DESTINATION_TYPE", "test-cycle")
	t.Setenv("PLUGIN_TEST_CYCLE_ID", "456")
}

func TestLoadConfigDefaults(t *testing.T) {
	setMinimumEnv(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example.qtestnet.com" || cfg.ProjectID != 123 {
		t.Fatalf("unexpected base config: %#v", cfg)
	}
	if cfg.DestinationType != "test-cycle" || cfg.DestinationID != 456 {
		t.Fatalf("unexpected destination: %#v", cfg)
	}
	if len(cfg.ResultPaths) != 1 || cfg.ResultPaths[0] != defaultResultPath {
		t.Fatalf("unexpected result paths: %#v", cfg.ResultPaths)
	}
	if cfg.IdentityMode != "class" || cfg.EmptyResult != "warn" || !cfg.ReuseSuite {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.BatchSize != defaultBatchSize || cfg.MaxPayloadBytes != defaultMaxPayloadBytes {
		t.Fatalf("unexpected batching defaults: %#v", cfg)
	}
	if cfg.PollInterval != defaultPollInterval || cfg.Timeout != defaultTimeout {
		t.Fatalf("unexpected timing defaults: %#v", cfg)
	}
}

func TestLoadConfigReleaseAndProperties(t *testing.T) {
	setMinimumEnv(t)
	t.Setenv("PLUGIN_DESTINATION_TYPE", "release")
	t.Setenv("PLUGIN_RELEASE_ID", "789")
	t.Setenv("PLUGIN_TEST_CYCLE_ID", "")
	t.Setenv("PLUGIN_SUITE_NAME", "Plan 10-06-2026")
	t.Setenv("PLUGIN_RESULT_PATHS", `reports\**\*.xml, other/*.xml`)
	t.Setenv("PLUGIN_IDENTITY_MODE", "method")
	t.Setenv("PLUGIN_ENVIRONMENT", "Windows 2025")
	t.Setenv("PLUGIN_ENVIRONMENT_FIELD_ID", "44")
	t.Setenv("PLUGIN_PROPERTIES", `[{"field_id":43,"field_value":"CI"}]`)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DestinationID != 789 || cfg.SuiteName != "Plan 10-06-2026" {
		t.Fatalf("unexpected release config: %#v", cfg)
	}
	if len(cfg.ResultPaths) != 2 || len(cfg.Properties) != 2 {
		t.Fatalf("unexpected list config: %#v", cfg)
	}
}

func TestConfigRejectsUnsafeOrInvalidValues(t *testing.T) {
	valid := Config{
		BaseURL: "https://example.qtestnet.com", Token: "token", ProjectID: 1,
		DestinationType: "test-cycle", DestinationID: 2, IdentityMode: "class",
		EmptyResult: "warn", BatchSize: 100, MaxPayloadBytes: 4096,
		PollInterval: time.Second, Timeout: time.Minute,
		Statuses: StatusMap{Passed: "PASSED", Failed: "FAILED", Error: "FAILED", Skipped: "SKIPPED"},
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		match  string
	}{
		{"http URL", func(c *Config) { c.BaseURL = "http://example.com" }, "HTTPS"},
		{"embedded credential", func(c *Config) { c.BaseURL = "https://u:p@example.com" }, "HTTPS"},
		{"missing token", func(c *Config) { c.Token = "" }, "TOKEN"},
		{"bad destination", func(c *Config) { c.DestinationType = "folder" }, "unsupported"},
		{"release without suite", func(c *Config) { c.DestinationType = "release" }, "SUITE_NAME"},
		{"bad identity", func(c *Config) { c.IdentityMode = "file" }, "IDENTITY_MODE"},
		{"bad CA", func(c *Config) { c.CACert = "not pem" }, "CA_CERT"},
		{"bad property", func(c *Config) { c.Properties = []Property{{FieldID: -1}} }, "field_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(strings.ToUpper(err.Error()), strings.ToUpper(test.match)) {
				t.Fatalf("expected %q error, got %v", test.match, err)
			}
		})
	}
}

func TestTokenIsNeverDerivedFromUnrelatedEnvironment(t *testing.T) {
	setMinimumEnv(t)
	t.Setenv("PLUGIN_BEARER_TOKEN", "")
	t.Setenv("PASSWORD", "should-not-be-used")
	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "BEARER_TOKEN") {
		t.Fatalf("expected missing token error, got %v", err)
	}
	if os.Getenv("PASSWORD") == "" {
		t.Fatal("test environment was unexpectedly cleared")
	}
}
