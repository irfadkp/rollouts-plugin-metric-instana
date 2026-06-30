package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/utils/evaluate"
	metricutil "github.com/argoproj/argo-rollouts/utils/metric"
	timeutil "github.com/argoproj/argo-rollouts/utils/time"
	"github.com/argoproj/argo-rollouts/utils/plugin/types"
	log "github.com/sirupsen/logrus"
)

// PluginName is the name used in the ConfigMap and AnalysisTemplate
const PluginName = "argoproj-labs/rollouts-plugin-metric-instana"

// MetricTypeApplication queries Instana application monitoring metrics
const MetricTypeApplication = "application"

// MetricTypeInfrastructure queries Instana infrastructure monitoring metrics
const MetricTypeInfrastructure = "infrastructure"

// Env var names for credential fallback
const (
	EnvInstanaEndpoint = "INSTANA_ENDPOINT"
	EnvInstanaAPIToken = "INSTANA_API_TOKEN"
)

// Ensure RpcPlugin satisfies the interface
var _ rpc.MetricProviderPlugin = &RpcPlugin{}

// RpcPlugin is the implementation of the MetricProviderPlugin interface for Instana
type RpcPlugin struct {
	LogCtx log.Entry
}

// Config holds the per-metric configuration provided inside the AnalysisTemplate
// under provider.plugin["argoproj-labs/rollouts-plugin-metric-instana"]
type Config struct {
	// Endpoint is the Instana backend base URL (e.g. https://unit-name.instana.io).
	// Falls back to env var INSTANA_ENDPOINT if not set.
	Endpoint string `json:"endpoint,omitempty"`

	// APIToken is the Instana API token.
	// Falls back to env var INSTANA_API_TOKEN if not set.
	APIToken string `json:"apiToken,omitempty"`

	// MetricType is "application" or "infrastructure"
	MetricType string `json:"metricType"`

	// MetricID is the Instana metric identifier (e.g. "calls.erroneous.rate", "cpu.user")
	MetricID string `json:"metricId"`

	// Query is an optional Instana Dynamic Focus query string to scope the metric
	Query string `json:"query,omitempty"`

	// Aggregation is the aggregation function: mean (default), sum, min, max, p50, p75, p90, p95, p98, p99
	Aggregation string `json:"aggregation,omitempty"`

	// RollupInterval is the aggregation window in seconds (default: 60)
	RollupInterval int32 `json:"rollupInterval,omitempty"`
}

// ---- Instana REST API request/response types --------------------------------

type timeFrame struct {
	To       int64 `json:"to"`
	Duration int64 `json:"duration"`
}

type metricQuery struct {
	Metric      string `json:"metric"`
	Aggregation string `json:"aggregation"`
	Granularity int32  `json:"granularity"`
}

type appMetricRequest struct {
	TimeFrame  timeFrame     `json:"timeFrame"`
	Metrics    []metricQuery `json:"metrics"`
	TagFilters []tagFilter   `json:"tagFilters,omitempty"`
}

type infraMetricRequest struct {
	TimeFrame timeFrame     `json:"timeFrame"`
	Metrics   []metricQuery `json:"metrics"`
	Query     string        `json:"query,omitempty"`
}

type tagFilter struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// Both application and infrastructure responses share this shape
type metricResponse struct {
	Items []struct {
		Metrics map[string][][2]*float64 `json:"metrics"`
	} `json:"items"`
}

// ---- MetricProviderPlugin implementation ------------------------------------

// InitPlugin is called once when the plugin starts. Nothing to initialise here.
func (g *RpcPlugin) InitPlugin() types.RpcError {
	return types.RpcError{}
}

// Run performs the Instana metric query and returns a Measurement
func (g *RpcPlugin) Run(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric) v1alpha1.Measurement {
	startTime := timeutil.MetaNow()
	measurement := v1alpha1.Measurement{StartedAt: &startTime}

	// Unmarshal the plugin config from the AnalysisTemplate
	cfg := Config{}
	if err := json.Unmarshal(metric.Provider.Plugin[PluginName], &cfg); err != nil {
		return metricutil.MarkMeasurementError(measurement,
			fmt.Errorf("instana plugin: failed to parse config: %w", err))
	}

	// Resolve credentials (config > env vars)
	endpoint, apiToken, err := resolveCredentials(cfg)
	if err != nil {
		return metricutil.MarkMeasurementError(measurement, err)
	}

	if err := validateConfig(cfg); err != nil {
		return metricutil.MarkMeasurementError(measurement, err)
	}

	// Defaults
	aggregation := cfg.Aggregation
	if aggregation == "" {
		aggregation = "mean"
	}
	rollup := cfg.RollupInterval
	if rollup <= 0 {
		rollup = 60
	}

	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	windowMs := int64(rollup) * 1000

	var (
		value  string
		status v1alpha1.AnalysisPhase
	)

	switch cfg.MetricType {
	case MetricTypeApplication:
		value, status, err = queryApplicationMetrics(endpoint, apiToken, cfg, nowMs, windowMs, aggregation, rollup, metric, g.LogCtx)
	case MetricTypeInfrastructure:
		value, status, err = queryInfrastructureMetrics(endpoint, apiToken, cfg, nowMs, windowMs, aggregation, rollup, metric, g.LogCtx)
	default:
		err = fmt.Errorf("instana plugin: unsupported metricType %q, must be %q or %q",
			cfg.MetricType, MetricTypeApplication, MetricTypeInfrastructure)
	}

	if err != nil {
		return metricutil.MarkMeasurementError(measurement, err)
	}

	measurement.Value = value
	measurement.Phase = status
	finishedTime := timeutil.MetaNow()
	measurement.FinishedAt = &finishedTime
	return measurement
}

// Resume is a no-op — all work is done synchronously in Run
func (g *RpcPlugin) Resume(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

// Terminate is a no-op
func (g *RpcPlugin) Terminate(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

// GarbageCollect is a no-op
func (g *RpcPlugin) GarbageCollect(_ *v1alpha1.AnalysisRun, _ v1alpha1.Metric, _ int) types.RpcError {
	return types.RpcError{}
}

// Type returns the plugin type string
func (g *RpcPlugin) Type() string {
	return PluginName
}

// GetMetadata returns additional metadata stored alongside the measurement result
func (g *RpcPlugin) GetMetadata(metric v1alpha1.Metric) map[string]string {
	meta := make(map[string]string)
	cfg := Config{}
	if err := json.Unmarshal(metric.Provider.Plugin[PluginName], &cfg); err == nil {
		if cfg.MetricID != "" {
			meta["instanaMetricId"] = cfg.MetricID
		}
		if cfg.MetricType != "" {
			meta["instanaMetricType"] = cfg.MetricType
		}
		if cfg.Query != "" {
			meta["instanaQuery"] = cfg.Query
		}
	}
	return meta
}

// ---- internal helpers -------------------------------------------------------

func resolveCredentials(cfg Config) (endpoint, apiToken string, err error) {
	endpoint = cfg.Endpoint
	if endpoint == "" {
		endpoint = os.Getenv(EnvInstanaEndpoint)
	}
	apiToken = cfg.APIToken
	if apiToken == "" {
		apiToken = os.Getenv(EnvInstanaAPIToken)
	}
	if endpoint == "" || apiToken == "" {
		return "", "", fmt.Errorf("instana plugin: endpoint and apiToken are required (set in config or via %s / %s env vars)",
			EnvInstanaEndpoint, EnvInstanaAPIToken)
	}
	return endpoint, apiToken, nil
}

func validateConfig(cfg Config) error {
	if cfg.MetricID == "" {
		return fmt.Errorf("instana plugin: metricId is required")
	}
	if cfg.MetricType == "" {
		return fmt.Errorf("instana plugin: metricType is required (\"application\" or \"infrastructure\")")
	}
	if cfg.MetricType != MetricTypeApplication && cfg.MetricType != MetricTypeInfrastructure {
		return fmt.Errorf("instana plugin: metricType %q is invalid, must be %q or %q",
			cfg.MetricType, MetricTypeApplication, MetricTypeInfrastructure)
	}
	return nil
}

func queryApplicationMetrics(endpoint, apiToken string, cfg Config, nowMs, windowMs int64, aggregation string, rollup int32, metric v1alpha1.Metric, logCtx log.Entry) (string, v1alpha1.AnalysisPhase, error) {
	req := appMetricRequest{
		TimeFrame: timeFrame{To: nowMs, Duration: windowMs},
		Metrics:   []metricQuery{{Metric: cfg.MetricID, Aggregation: aggregation, Granularity: rollup}},
	}
	if cfg.Query != "" {
		req.TagFilters = []tagFilter{{Name: "dynamic.focus.query", Operator: "EQUALS", Value: cfg.Query}}
	}

	respBytes, err := doPost(endpoint+"/api/application-monitoring/metrics/applications", apiToken, req)
	if err != nil {
		return "", v1alpha1.AnalysisPhaseError, err
	}

	var resp metricResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", v1alpha1.AnalysisPhaseError, fmt.Errorf("instana plugin: failed to parse application metrics response: %w", err)
	}
	return extractValue(resp.Items, cfg.MetricID, metric, logCtx)
}

func queryInfrastructureMetrics(endpoint, apiToken string, cfg Config, nowMs, windowMs int64, aggregation string, rollup int32, metric v1alpha1.Metric, logCtx log.Entry) (string, v1alpha1.AnalysisPhase, error) {
	req := infraMetricRequest{
		TimeFrame: timeFrame{To: nowMs, Duration: windowMs},
		Metrics:   []metricQuery{{Metric: cfg.MetricID, Aggregation: aggregation, Granularity: rollup}},
		Query:     cfg.Query,
	}

	respBytes, err := doPost(endpoint+"/api/infrastructure-monitoring/metrics", apiToken, req)
	if err != nil {
		return "", v1alpha1.AnalysisPhaseError, err
	}

	var resp metricResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", v1alpha1.AnalysisPhaseError, fmt.Errorf("instana plugin: failed to parse infrastructure metrics response: %w", err)
	}
	return extractValue(resp.Items, cfg.MetricID, metric, logCtx)
}

func doPost(url, apiToken string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("instana plugin: failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("instana plugin: failed to build HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "apiToken "+apiToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("instana plugin: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("instana plugin: failed to read response body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("instana plugin: authentication error (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instana plugin: unexpected HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}

func extractValue(items []struct {
	Metrics map[string][][2]*float64 `json:"metrics"`
}, metricID string, metric v1alpha1.Metric, logCtx log.Entry) (string, v1alpha1.AnalysisPhase, error) {
	if len(items) == 0 {
		var nilFloat64 *float64
		status, err := evaluate.EvaluateResult(nilFloat64, metric, logCtx)
		return "[]", status, err
	}

	series, ok := items[0].Metrics[metricID]
	if !ok || len(series) == 0 {
		var nilFloat64 *float64
		status, err := evaluate.EvaluateResult(nilFloat64, metric, logCtx)
		return "[]", status, err
	}

	lastPoint := series[len(series)-1]
	if lastPoint[1] == nil {
		var nilFloat64 *float64
		status, err := evaluate.EvaluateResult(nilFloat64, metric, logCtx)
		return "null", status, err
	}

	val := *lastPoint[1]
	status, err := evaluate.EvaluateResult(val, metric, logCtx)
	return strconv.FormatFloat(val, 'f', -1, 64), status, err
}
