{{- define "ottawa-fleet.labels" -}}
app.kubernetes.io/name: ottawa-fleet
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end }}

{{- define "ottawa-fleet.telemetryEnv" -}}
{{- if .Values.telemetry.enabled -}}
{{- $endpointPattern := `^https?://([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)(\.([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?))*(:(6553[0-5]|655[0-2][0-9]|65[0-4][0-9]{2}|6[0-4][0-9]{3}|[1-5][0-9]{4}|[1-9][0-9]{0,3}))?$` -}}
{{- $endpoint := .Values.telemetry.otlpEndpoint -}}
{{- if or (not $endpoint) (not (regexMatch $endpointPattern $endpoint)) -}}
{{- fail "telemetry.enabled requires telemetry.otlpEndpoint to be an HTTP(S) origin URL with a valid host and optional port; paths, credentials, query strings, and fragments are not allowed" -}}
{{- end }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ $endpoint | quote }}
{{- end -}}
{{- end }}

{{- define "ottawa-fleet.selectorLabels" -}}
app.kubernetes.io/name: ottawa-fleet
app.kubernetes.io/instance: {{ .root.Release.Name | quote }}
app.kubernetes.io/component: {{ .component | quote }}
{{- end }}

{{- define "ottawa-fleet.image" -}}
{{ .repository }}@{{ .digest }}
{{- end }}

{{- define "ottawa-fleet.podAnnotations" -}}
{{- with .root.Values.podAnnotations }}{{ toYaml . }}{{ end }}
app.ottawa-fleet/source-commit: {{ .image.sourceCommit | quote }}
app.ottawa-fleet/architecture: {{ .image.architecture | quote }}
app.ottawa-fleet/image-digest: {{ .image.digest | quote }}
{{- end }}

{{- define "ottawa-fleet.databaseEnv" -}}
{{- if .Values.database.disposable.enabled }}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.disposable.connectionSecret | quote }}
      key: {{ .Values.database.disposable.connectionSecretKey | quote }}
{{- else }}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret | quote }}
      key: {{ .Values.database.secretKey | quote }}
{{- end }}
{{- end }}

{{- define "ottawa-fleet.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
{{ toYaml . | indent 2 }}
{{- end }}
{{- end }}
