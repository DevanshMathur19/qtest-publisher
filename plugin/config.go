package plugin

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultResultPath      = "**/junit/*.xml"
	defaultBatchSize       = 100
	defaultMaxPayloadBytes = 4 * 1024 * 1024
	defaultMaxTestCases    = 1_000_000
	defaultPollInterval    = 2 * time.Second
	defaultTimeout         = 10 * time.Minute
)

type Property struct {
	FieldID        int64  `json:"field_id"`
	FieldName      string `json:"field_name,omitempty"`
	FieldValue     string `json:"field_value,omitempty"`
	FieldValueName string `json:"field_value_name,omitempty"`
}

type StatusMap struct {
	Passed  string
	Failed  string
	Error   string
	Skipped string
}

type Config struct {
	BaseURL         string
	Token           string
	ProjectID       int64
	ResultPaths     []string
	DestinationType string
	DestinationID   int64
	SuiteName       string
	ReuseSuite      bool
	IdentityMode    string
	ModuleNames     []string
	Statuses        StatusMap
	Properties      []Property
	EmptyResult     string
	BatchSize       int
	MaxPayloadBytes int
	MaxTestCases    int
	PollInterval    time.Duration
	Timeout         time.Duration
	CACert          string
	ProxyURL        string
	OutputPath      string
	BuildNumber     string
	BuildURL        string
	ExecutionDate   time.Time
	InsecureSkipTLS bool
}

func LoadConfig() (Config, error) {
	cfg := Config{
		BaseURL:         firstEnv("PLUGIN_QTEST_URL", "PLUGIN_URL"),
		Token:           firstEnv("PLUGIN_BEARER_TOKEN", "PLUGIN_API_TOKEN", "PLUGIN_TOKEN"),
		ResultPaths:     splitList(firstEnv("PLUGIN_RESULT_PATHS", "PLUGIN_RESULT_PATH")),
		DestinationType: strings.ToLower(strings.TrimSpace(firstEnv("PLUGIN_DESTINATION_TYPE"))),
		SuiteName:       strings.TrimSpace(firstEnv("PLUGIN_SUITE_NAME")),
		IdentityMode:    strings.ToLower(strings.TrimSpace(firstEnv("PLUGIN_IDENTITY_MODE"))),
		ModuleNames:     splitList(firstEnv("PLUGIN_MODULE_NAMES")),
		EmptyResult:     strings.ToLower(strings.TrimSpace(firstEnv("PLUGIN_EMPTY_RESULT"))),
		CACert:          firstEnv("PLUGIN_CA_CERT", "PLUGIN_PEM_FILE_CONTENTS"),
		ProxyURL:        firstEnv("PLUGIN_PROXY_URL", "HARNESS_HTTPS_PROXY", "HTTPS_PROXY"),
		OutputPath:      os.Getenv("DRONE_OUTPUT"),
		BuildNumber:     firstEnv("DRONE_BUILD_NUMBER", "CI_BUILD_NUMBER"),
		BuildURL:        firstEnv("DRONE_BUILD_LINK", "CI_BUILD_URL"),
		ExecutionDate:   time.Now().UTC(),
	}

	var err error
	if cfg.ProjectID, err = parseRequiredInt64("PLUGIN_PROJECT_ID"); err != nil {
		return Config{}, err
	}
	if cfg.DestinationID, err = destinationID(cfg.DestinationType); err != nil {
		return Config{}, err
	}
	if cfg.ReuseSuite, err = boolEnv("PLUGIN_REUSE_SUITE", true); err != nil {
		return Config{}, err
	}
	if cfg.InsecureSkipTLS, err = boolEnv("PLUGIN_INSECURE_SKIP_TLS", false); err != nil {
		return Config{}, err
	}
	if cfg.BatchSize, err = intEnv("PLUGIN_BATCH_SIZE", defaultBatchSize); err != nil {
		return Config{}, err
	}
	if cfg.MaxPayloadBytes, err = intEnv("PLUGIN_MAX_PAYLOAD_BYTES", defaultMaxPayloadBytes); err != nil {
		return Config{}, err
	}
	if cfg.MaxTestCases, err = intEnv("PLUGIN_MAX_TEST_CASES", defaultMaxTestCases); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = durationEnv("PLUGIN_POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}
	if cfg.Timeout, err = durationEnv("PLUGIN_TIMEOUT", defaultTimeout); err != nil {
		return Config{}, err
	}

	cfg.Statuses = StatusMap{
		Passed:  envDefault("PLUGIN_STATUS_PASSED", "PASSED"),
		Failed:  envDefault("PLUGIN_STATUS_FAILED", "FAILED"),
		Error:   envDefault("PLUGIN_STATUS_ERROR", "FAILED"),
		Skipped: envDefault("PLUGIN_STATUS_SKIPPED", "SKIPPED"),
	}
	if raw := strings.TrimSpace(firstEnv("PLUGIN_PROPERTIES")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Properties); err != nil {
			return Config{}, fmt.Errorf("PLUGIN_PROPERTIES must be a JSON property array: %w", err)
		}
	}
	if environment := strings.TrimSpace(firstEnv("PLUGIN_ENVIRONMENT")); environment != "" {
		fieldID, err := parseRequiredInt64("PLUGIN_ENVIRONMENT_FIELD_ID")
		if err != nil {
			return Config{}, fmt.Errorf("PLUGIN_ENVIRONMENT requires PLUGIN_ENVIRONMENT_FIELD_ID: %w", err)
		}
		cfg.Properties = append(cfg.Properties, Property{
			FieldID:        fieldID,
			FieldName:      "Environment",
			FieldValue:     environment,
			FieldValueName: environment,
		})
	}

	if len(cfg.ResultPaths) == 0 {
		cfg.ResultPaths = []string{defaultResultPath}
	}
	if cfg.DestinationType == "" {
		cfg.DestinationType = "test-cycle"
	}
	if cfg.IdentityMode == "" {
		cfg.IdentityMode = "class"
	}
	if cfg.EmptyResult == "" {
		cfg.EmptyResult = "warn"
	}
	if len(cfg.ModuleNames) == 0 {
		cfg.ModuleNames = []string{"Automation"}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("PLUGIN_QTEST_URL must be an HTTPS URL without embedded credentials")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("PLUGIN_BEARER_TOKEN is required")
	}
	if c.ProjectID <= 0 {
		return errors.New("PLUGIN_PROJECT_ID must be positive")
	}
	switch c.DestinationType {
	case "release":
		if strings.TrimSpace(c.SuiteName) == "" {
			return errors.New("PLUGIN_SUITE_NAME is required for a release destination")
		}
	case "test-cycle", "test-suite", "test-run":
	default:
		return fmt.Errorf("unsupported PLUGIN_DESTINATION_TYPE %q", c.DestinationType)
	}
	if c.DestinationID <= 0 {
		return errors.New("destination ID must be positive")
	}
	if c.IdentityMode != "class" && c.IdentityMode != "method" {
		return errors.New("PLUGIN_IDENTITY_MODE must be class or method")
	}
	if c.EmptyResult != "warn" && c.EmptyResult != "fail" {
		return errors.New("PLUGIN_EMPTY_RESULT must be warn or fail")
	}
	if c.BatchSize < 1 || c.BatchSize > 1000 {
		return errors.New("PLUGIN_BATCH_SIZE must be between 1 and 1000")
	}
	if c.MaxPayloadBytes < 1024 || c.MaxPayloadBytes > 50*1024*1024 {
		return errors.New("PLUGIN_MAX_PAYLOAD_BYTES must be between 1024 and 52428800")
	}
	maxTestCases := c.MaxTestCases
	if maxTestCases == 0 {
		maxTestCases = defaultMaxTestCases
	}
	if maxTestCases < 1 || maxTestCases > 5_000_000 {
		return errors.New("PLUGIN_MAX_TEST_CASES must be between 1 and 5000000")
	}
	if c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return errors.New("PLUGIN_POLL_INTERVAL must be between 100ms and 1m")
	}
	if c.Timeout < c.PollInterval || c.Timeout > 24*time.Hour {
		return errors.New("PLUGIN_TIMEOUT must be at least the poll interval and no more than 24h")
	}
	for name, status := range map[string]string{
		"passed": c.Statuses.Passed, "failed": c.Statuses.Failed,
		"error": c.Statuses.Error, "skipped": c.Statuses.Skipped,
	} {
		if strings.TrimSpace(status) == "" {
			return fmt.Errorf("%s status mapping cannot be empty", name)
		}
	}
	if c.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.CACert)) {
			return errors.New("PLUGIN_CA_CERT does not contain a valid PEM certificate")
		}
	}
	if c.ProxyURL != "" {
		proxy, err := url.Parse(c.ProxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") {
			return errors.New("proxy URL must use http or https")
		}
	}
	for _, property := range c.Properties {
		if property.FieldID <= 0 {
			return errors.New("property field_id must be positive")
		}
	}
	return nil
}

func destinationID(destinationType string) (int64, error) {
	typeName := strings.ToLower(strings.TrimSpace(destinationType))
	if typeName == "" {
		typeName = "test-cycle"
	}
	names := map[string][]string{
		"release":    {"PLUGIN_RELEASE_ID", "PLUGIN_DESTINATION_ID"},
		"test-cycle": {"PLUGIN_TEST_CYCLE_ID", "PLUGIN_DESTINATION_ID"},
		"test-suite": {"PLUGIN_TEST_SUITE_ID", "PLUGIN_DESTINATION_ID"},
		"test-run":   {"PLUGIN_TEST_RUN_ID", "PLUGIN_DESTINATION_ID"},
	}
	candidates, ok := names[typeName]
	if !ok {
		return 0, fmt.Errorf("unsupported PLUGIN_DESTINATION_TYPE %q", typeName)
	}
	for _, name := range candidates {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return parseRequiredInt64(name)
		}
	}
	return 0, errors.New("a destination ID is required")
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			return value
		}
	}
	return ""
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseRequiredInt64(name string) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func intEnv(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 2s or 10m", name)
	}
	return parsed, nil
}
