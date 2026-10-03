# Local telemetry

Optional local tracing and log collection uses the platform repository's Grafana, Loki, Tempo, Mimir, and Alloy Compose stack. The application continues to write JSON logs to stdout. With the telemetry overlay, Fleet API, Ride API, and assignment worker also mirror JSONL logs to a shared named volume (8 MiB per service), which Alloy mounts read-only. A one-shot BusyBox init service sets that named volume's ownership to the app's non-root UID before the app starts. Failure to open a mirror file leaves stdout logging enabled and does not prevent startup. The application uses OTLP/HTTP for traces and Alloy scrapes the existing `/metrics` endpoints every 15 seconds. HTTP request completion logs include the request ID and trace/span IDs for cross-signal lookup; no request bodies, query strings, ride IDs, or job IDs are trace attributes. HTTP route names use stable route templates.

The application uses OpenTelemetry Go SDK and OTLP/HTTP exporter `v1.43.0` ([Apache-2.0 source](https://github.com/open-telemetry/opentelemetry-go)). The upstream [Go exporter guide](https://opentelemetry.io/docs/languages/go/exporters/) describes the OTLP exporter setup.

The asynchronous ride-to-worker handoff does not currently persist a trace context in the job record. The worker creates its own process trace and includes its trace/span IDs with existing request/job identifiers in the processing log. The worker trace follows outbound Fleet HTTP calls through W3C `traceparent`; the originating Ride API request remains a separate trace, linked operationally by the existing request ID in logs.

## Start

Run the base app once to create its named bridge network, then apply the app telemetry overlay so its named log volume is created before the platform stack declares it external. The app can start before Alloy: OTLP export is best-effort and queued within bounded memory until delivery or expiry. Start the platform stack after the overlay.

```powershell
# From ottawa-fleet-app: creates ottawa-fleet-app bridge network
docker compose -f deploy/compose.yaml up -d --build

# From ottawa-fleet-app: creates/initializes the shared JSONL volume
docker compose -f deploy/compose.yaml -f deploy/compose.telemetry.yaml up -d --build

# From ottawa-fleet-platform; use a local-only password and keep it out of files
$env:GRAFANA_ADMIN_PASSWORD = 'choose-a-local-password'
docker compose -f observability/compose.telemetry.yaml up -d
```

Open [Grafana](http://127.0.0.1:3000) and sign in as `admin`. The preloaded **Ottawa Fleet local telemetry** dashboard queries the current pending-job and completed-job metrics, then shows recent application logs. Structured completion logs carry trace/span IDs: Loki offers a TraceID link, while Tempo can search Loki using those IDs. The Mimir, Loki, and Tempo HTTP ports are container-network-only. Grafana binds to loopback. Alloy's optional host OTLP/HTTP receiver also binds only to loopback; application containers use `alloy:4318` on their shared bridge networks.

Stop the app with `docker compose -f deploy/compose.yaml -f deploy/compose.telemetry.yaml down` and the platform services with `docker compose -f observability/compose.telemetry.yaml down`. The `ottawa-fleet-app-telemetry-logs` volume retains the app's JSONL files and the platform volumes retain backend history. Remove the app log volume with `docker volume rm ottawa-fleet-app-telemetry-logs` only when intentionally discarding those files; Compose recreates and reinitializes it on the next overlay start. Each file stops growing at 8 MiB, after which those events remain on stdout only and Alloy stops collecting that service's later file logs.

## Scope and limits

The local process limits total 3 CPU and 2,176 MiB memory across Grafana (0.5 CPU/384 MiB), Loki (0.5/384), Tempo (0.5/384), Mimir (1/768), and Alloy (0.5/256). This is a configured ceiling, not a measured resource baseline; Compose-engine enforcement must be confirmed on the host. Retention is 48 hours for Loki and 24 hours for Tempo and Mimir. Compaction/deletion is asynchronous, and local filesystem storage has no portable Compose disk quota, so the retention settings do not establish a hard disk ceiling. No cloud, cluster, or production deployment is part of this setup.

The OTel SDK/exporter is initialized when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Normal app Compose and local tests leave export disabled. Invalid exporter configuration disables trace export with a warning and falls back to the local provider. Trace batching is limited to 256 queued spans and 64 spans per export batch, export timeout is two seconds, and shutdown allows three seconds for a flush. Metrics remain the existing Prometheus exposition endpoints; this does not introduce an OTel metrics SDK or change API/job contracts.
