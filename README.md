# rollouts-plugin-metric-instana

An [Argo Rollouts](https://argoproj.github.io/argo-rollouts/) metric plugin that integrates **IBM Instana** as an analysis provider for canary deployments.

Both **application monitoring** metrics (call latency, error rate, throughput) and **infrastructure monitoring** metrics (CPU, memory, JVM, etc.) are supported.

---

## Installation

### 1. Add the plugin to the Argo Rollouts ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
  namespace: argo-rollouts
data:
  metricProviderPlugins: |-
    - name: "argoproj-labs/rollouts-plugin-metric-instana"
      location: "https://github.com/argoproj-labs/rollouts-plugin-metric-instana/releases/download/v0.1.0/rollouts-plugin-metric-instana-linux-amd64"
      sha256: "<sha256-of-the-binary>"
```

### 2. Create an AnalysisTemplate

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: instana-error-rate
spec:
  args:
  - name: service-name
  metrics:
  - name: error-rate
    interval: 2m
    successCondition: default(result, 0) < 0.01
    failureCondition: default(result, 0) >= 0.05
    failureLimit: 3
    provider:
      plugin:
        argoproj-labs/rollouts-plugin-metric-instana:
          endpoint: https://<unit-name>.instana.io
          apiToken: <your-instana-api-token>
          metricType: application
          metricId: calls.erroneous.rate
          query: "entity.application.name:{{args.service-name}}"
          aggregation: mean
          rollupInterval: 120
```

---

## Configuration Reference

| Field | Required | Default | Description |
|---|---|---|---|
| `endpoint` | ✓* | `$INSTANA_ENDPOINT` | Instana backend URL (e.g. `https://unit-name.instana.io`) |
| `apiToken` | ✓* | `$INSTANA_API_TOKEN` | Instana API token with read access |
| `metricType` | ✓ | — | `application` or `infrastructure` |
| `metricId` | ✓ | — | Instana metric identifier (e.g. `calls.erroneous.rate`) |
| `query` | | — | [Dynamic Focus](https://www.ibm.com/docs/en/instana-observability/current?topic=instana-filtering-dynamic-focus) query to scope the metric |
| `aggregation` | | `mean` | `mean`, `sum`, `min`, `max`, `p50`, `p75`, `p90`, `p95`, `p98`, `p99` |
| `rollupInterval` | | `60` | Aggregation window in seconds |

\* Can be provided via environment variables `INSTANA_ENDPOINT` and `INSTANA_API_TOKEN` instead of inline config.

---

## Credential Options

Credentials are resolved in this order:

1. **Inline config** — `endpoint` and `apiToken` fields in the AnalysisTemplate
2. **Environment variables** — `INSTANA_ENDPOINT` and `INSTANA_API_TOKEN` on the rollouts controller pod

---

## Common Application Metric IDs

| Metric ID | Description |
|---|---|
| `calls.count` | Total call count |
| `calls.erroneous.count` | Erroneous call count |
| `calls.erroneous.rate` | Error rate |
| `calls.latency.mean` | Mean latency (ms) |
| `calls.latency.p99` | p99 latency (ms) |

## Common Infrastructure Metric IDs

| Metric ID | Description |
|---|---|
| `cpu.user` | CPU user utilisation (%) |
| `cpu.sys` | CPU system utilisation (%) |
| `memory.used` | Memory used (bytes) |
| `jvm.memory.heap.used` | JVM heap used (bytes) |

---

## Building from source

```bash
make build
# binary at: bin/rollouts-plugin-metric-instana
```

## Running tests

```bash
make test
```

---

## Examples

See the [`examples/`](examples/) directory for ready-to-use YAML manifests.

---

## License

Apache 2.0
