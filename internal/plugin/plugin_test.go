package plugin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
)

func newPlugin() *RpcPlugin {
	return &RpcPlugin{LogCtx: *log.WithField("test", "instana")}
}

func newMetric(t *testing.T, serverURL string, cfg Config, successCondition, failureCondition string) v1alpha1.Metric {
	t.Helper()
	if cfg.Endpoint == "" {
		cfg.Endpoint = serverURL
	}
	if cfg.APIToken == "" {
		cfg.APIToken = "test-token"
	}
	raw, err := json.Marshal(cfg)
	assert.NoError(t, err)
	return v1alpha1.Metric{
		Name:             "test",
		SuccessCondition: successCondition,
		FailureCondition: failureCondition,
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{
				PluginName: raw,
			},
		},
	}
}

func appResponse(metricID string, value float64) string {
	resp := map[string]any{
		"items": []any{
			map[string]any{
				"metrics": map[string]any{
					metricID: [][2]any{{1698000000000.0, value}},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func emptyResponse() string {
	b, _ := json.Marshal(map[string]any{"items": []any{}})
	return string(b)
}

func newAnalysisRun() *v1alpha1.AnalysisRun { return &v1alpha1.AnalysisRun{} }

// --------------------------------------------------------------------------
// InitPlugin
// --------------------------------------------------------------------------

func TestInitPlugin(t *testing.T) {
	p := newPlugin()
	err := p.InitPlugin()
	assert.False(t, err.HasError())
}

// --------------------------------------------------------------------------
// Type / GetMetadata
// --------------------------------------------------------------------------

func TestType(t *testing.T) {
	p := newPlugin()
	assert.Equal(t, PluginName, p.Type())
}

func TestGetMetadata(t *testing.T) {
	p := newPlugin()
	cfg := Config{MetricType: MetricTypeApplication, MetricID: "calls.latency.p99", Query: "entity.application.name:svc"}
	raw, _ := json.Marshal(cfg)
	metric := v1alpha1.Metric{
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{PluginName: raw},
		},
	}
	meta := p.GetMetadata(metric)
	assert.Equal(t, "calls.latency.p99", meta["instanaMetricId"])
	assert.Equal(t, MetricTypeApplication, meta["instanaMetricType"])
	assert.Equal(t, "entity.application.name:svc", meta["instanaQuery"])
}

// --------------------------------------------------------------------------
// validateConfig
// --------------------------------------------------------------------------

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"missing metricId", Config{MetricType: MetricTypeApplication}, "metricId is required"},
		{"missing metricType", Config{MetricID: "calls.latency.p99"}, "metricType is required"},
		{"invalid metricType", Config{MetricID: "calls.latency.p99", MetricType: "unknown"}, "invalid"},
		{"valid application", Config{MetricID: "calls.latency.p99", MetricType: MetricTypeApplication}, ""},
		{"valid infrastructure", Config{MetricID: "cpu.user", MetricType: MetricTypeInfrastructure}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Run — application metrics
// --------------------------------------------------------------------------

func TestRunApplicationMetrics(t *testing.T) {
	const metricID = "calls.erroneous.rate"

	tests := []struct {
		name             string
		serverStatus     int
		serverResponse   string
		successCondition string
		failureCondition string
		expectedPhase    v1alpha1.AnalysisPhase
		expectedValue    string
		expectedErrSub   string
	}{
		{
			name:             "success: value satisfies condition",
			serverStatus:     http.StatusOK,
			serverResponse:   appResponse(metricID, 0.005),
			successCondition: "result < 0.01",
			expectedPhase:    v1alpha1.AnalysisPhaseSuccessful,
			expectedValue:    "0.005",
		},
		{
			name:             "failure: value violates condition",
			serverStatus:     http.StatusOK,
			serverResponse:   appResponse(metricID, 0.2),
			successCondition: "result < 0.01",
			failureCondition: "result >= 0.01",
			expectedPhase:    v1alpha1.AnalysisPhaseFailed,
			expectedValue:    "0.2",
		},
		{
			name:           "error: 401 unauthorized",
			serverStatus:   http.StatusUnauthorized,
			serverResponse: `{"errors":["Unauthorized"]}`,
			expectedPhase:  v1alpha1.AnalysisPhaseError,
			expectedErrSub: "authentication error",
		},
		{
			name:           "error: non-2xx response",
			serverStatus:   http.StatusBadRequest,
			serverResponse: `{"errors":["bad request"]}`,
			expectedPhase:  v1alpha1.AnalysisPhaseError,
			expectedErrSub: "unexpected HTTP 400",
		},
		{
			name:             "empty result handled with default()",
			serverStatus:     http.StatusOK,
			serverResponse:   emptyResponse(),
			successCondition: "default(result, 0) < 0.01",
			expectedPhase:    v1alpha1.AnalysisPhaseSuccessful,
			expectedValue:    "[]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				assert.Equal(t, "apiToken test-token", req.Header.Get("Authorization"))
				io.ReadAll(req.Body)
				if tc.serverStatus != http.StatusOK {
					http.Error(rw, tc.serverResponse, tc.serverStatus)
					return
				}
				rw.Header().Set("Content-Type", "application/json")
				io.WriteString(rw, tc.serverResponse)
			}))
			defer server.Close()

			p := newPlugin()
			cfg := Config{MetricType: MetricTypeApplication, MetricID: metricID, Aggregation: "mean", RollupInterval: 60}
			metric := newMetric(t, server.URL, cfg, tc.successCondition, tc.failureCondition)

			m := p.Run(newAnalysisRun(), metric)
			assert.Equal(t, string(tc.expectedPhase), string(m.Phase))
			if tc.expectedErrSub != "" {
				assert.Contains(t, m.Message, tc.expectedErrSub)
			} else {
				assert.Equal(t, tc.expectedValue, m.Value)
				assert.NotNil(t, m.StartedAt)
				assert.NotNil(t, m.FinishedAt)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Run — infrastructure metrics
// --------------------------------------------------------------------------

func TestRunInfrastructureMetrics(t *testing.T) {
	const metricID = "cpu.user"
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, appResponse(metricID, 23.5))
	}))
	defer server.Close()

	p := newPlugin()
	cfg := Config{MetricType: MetricTypeInfrastructure, MetricID: metricID}
	metric := newMetric(t, server.URL, cfg, "result < 80", "")

	m := p.Run(newAnalysisRun(), metric)
	assert.Equal(t, string(v1alpha1.AnalysisPhaseSuccessful), string(m.Phase))
	assert.Equal(t, "23.5", m.Value)
}

// --------------------------------------------------------------------------
// Credential resolution from env vars
// --------------------------------------------------------------------------

func TestCredentialsFromEnvVars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, appResponse("calls.latency.p99", 5.0))
	}))
	defer server.Close()

	os.Setenv(EnvInstanaEndpoint, server.URL)
	os.Setenv(EnvInstanaAPIToken, "env-token")
	defer func() {
		os.Unsetenv(EnvInstanaEndpoint)
		os.Unsetenv(EnvInstanaAPIToken)
	}()

	p := newPlugin()
	// No endpoint/apiToken in config — must come from env
	cfg := Config{MetricType: MetricTypeApplication, MetricID: "calls.latency.p99"}
	raw, _ := json.Marshal(cfg)
	metric := v1alpha1.Metric{
		Name:             "test",
		SuccessCondition: "result < 100",
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{PluginName: raw},
		},
	}

	m := p.Run(newAnalysisRun(), metric)
	assert.Equal(t, string(v1alpha1.AnalysisPhaseSuccessful), string(m.Phase))
}

// --------------------------------------------------------------------------
// Missing credentials
// --------------------------------------------------------------------------

func TestMissingCredentials(t *testing.T) {
	os.Unsetenv(EnvInstanaEndpoint)
	os.Unsetenv(EnvInstanaAPIToken)

	p := newPlugin()
	cfg := Config{MetricType: MetricTypeApplication, MetricID: "calls.latency.p99"}
	raw, _ := json.Marshal(cfg)
	metric := v1alpha1.Metric{
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{PluginName: raw},
		},
	}

	m := p.Run(newAnalysisRun(), metric)
	assert.Equal(t, string(v1alpha1.AnalysisPhaseError), string(m.Phase))
	assert.Contains(t, m.Message, "endpoint and apiToken are required")
}

// --------------------------------------------------------------------------
// No-ops
// --------------------------------------------------------------------------

func TestNoOps(t *testing.T) {
	p := newPlugin()
	meas := v1alpha1.Measurement{}
	assert.Equal(t, meas, p.Resume(newAnalysisRun(), v1alpha1.Metric{}, meas))
	assert.Equal(t, meas, p.Terminate(newAnalysisRun(), v1alpha1.Metric{}, meas))
	err := p.GarbageCollect(newAnalysisRun(), v1alpha1.Metric{}, 0)
	assert.False(t, err.HasError())
}

// --------------------------------------------------------------------------
// Bad JSON config
// --------------------------------------------------------------------------

func TestBadConfig(t *testing.T) {
	p := newPlugin()
	metric := v1alpha1.Metric{
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{
				PluginName: json.RawMessage(`{not valid json}`),
			},
		},
	}
	m := p.Run(newAnalysisRun(), metric)
	assert.Equal(t, string(v1alpha1.AnalysisPhaseError), string(m.Phase))
	assert.Contains(t, m.Message, "failed to parse config")
}

// suppress unused import warning
var _ = intstr.FromString
