{{/* Name helpers */}}
{{- define "sisyphus.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sisyphus.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "sisyphus.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sisyphus.labels" -}}
helm.sh/chart: {{ include "sisyphus.chart" . }}
app.kubernetes.io/name: {{ include "sisyphus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Usage: include "sisyphus.selectorLabels" (dict "root" $ "component" "ssapi") */}}
{{- define "sisyphus.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sisyphus.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "sisyphus.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sisyphus.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "sisyphus.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "sisyphus.secretName" -}}
{{- default (printf "%s-secrets" (include "sisyphus.fullname" .)) .Values.secrets.existingSecret }}
{{- end }}

{{/* Endpoints of the chart's own datastores, or the external overrides. */}}
{{- define "sisyphus.postgres.host" -}}
{{- printf "%s-postgres" (include "sisyphus.fullname" .) }}
{{- end }}

{{- define "sisyphus.postgres.dsn" -}}
{{- with .Values.postgres.external.dsn }}{{ . }}{{- else -}}
{{- $a := .Values.postgres.auth -}}
postgres://{{ $a.username }}:{{ $a.password }}@{{ include "sisyphus.postgres.host" . }}:5432/{{ $a.database }}?sslmode={{ .Values.postgres.sslmode }}
{{- end }}
{{- end }}

{{- define "sisyphus.qdrant.addr" -}}
{{- default (printf "%s-qdrant:6334" (include "sisyphus.fullname" .)) .Values.qdrant.external.addr }}
{{- end }}

{{- define "sisyphus.ollama.url" -}}
{{- default (printf "http://%s-ollama:11434" (include "sisyphus.fullname" .)) .Values.ollama.external.url }}
{{- end }}

{{- define "sisyphus.gateway.url" -}}
{{- printf "http://%s-mcpgateway:%v%s" (include "sisyphus.fullname" .) .Values.mcp.gateway.port .Values.mcp.gateway.path }}
{{- end }}

{{/* MCP endpoint of the sandbox, whichever mode it runs in. */}}
{{- define "sisyphus.sandbox.mcpURL" -}}
{{- $full := include "sisyphus.fullname" . -}}
{{- if eq .Values.sandbox.mode "mcp" -}}
http://{{ $full }}-sandbox:{{ .Values.sandbox.mcp.port }}{{ .Values.sandbox.mcp.path }}
{{- else -}}
http://{{ $full }}-ssh-mcp:{{ .Values.sshMcp.port }}{{ .Values.sshMcp.path }}
{{- end }}
{{- end }}

{{/* OTLP env, shared by every component that emits telemetry. */}}
{{- define "sisyphus.otelEnv" -}}
{{- if .Values.otelcol.enabled }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: http://{{ include "sisyphus.fullname" . }}-otelcol:4317
- name: OTEL_EXPORTER_OTLP_INSECURE
  value: "true"
- name: OTEL_EXPORTER_OTLP_PROTOCOL
  value: grpc
{{- end }}
{{- end }}

{{/* NO_PROXY for proxied pods. Suffix rules like ".svc.cluster.local" do not
     match the short Service names the chart puts in gateway.toml (no dots), so
     every chart Service is listed explicitly — otherwise a proxied pod would
     route its in-cluster upstream calls through the SOCKS proxy and time out. */}}
{{- define "sisyphus.noProxy" -}}
{{- $full := include "sisyphus.fullname" . -}}
{{- $names := list "postgres" "qdrant" "ollama" "ssapi" "ssingest" "ssbot" "ssagent" "ssmcp" "mcpgateway" "sandbox" "ssh-mcp" "otelcol" "alertmanager" "vmalert" -}}
{{- range $name, $srv := .Values.mcp.servers }}{{- if $srv.enabled }}{{ $names = append $names (printf "mcp-%s" $name) }}{{ end }}{{ end }}
{{- $hosts := list .Values.proxy.noProxy -}}
{{- range $names }}{{ $hosts = append $hosts (printf "%s-%s" $full .) }}{{ end }}
{{- join "," $hosts -}}
{{- end }}

{{/* Corporate egress proxy env. */}}
{{- define "sisyphus.proxyEnv" -}}
{{- if and .Values.proxy.enabled .Values.proxy.url }}
{{- $no := include "sisyphus.noProxy" . }}
- name: HTTP_PROXY
  value: {{ .Values.proxy.url | quote }}
- name: HTTPS_PROXY
  value: {{ .Values.proxy.url | quote }}
- name: ALL_PROXY
  value: {{ .Values.proxy.url | quote }}
- name: NO_PROXY
  value: {{ $no | quote }}
- name: no_proxy
  value: {{ $no | quote }}
{{- end }}
{{- end }}

{{/* Env shared by all sisyphus binaries: config path, every secret, OTLP. */}}
{{- define "sisyphus.appEnv" -}}
- name: SISYPHUS_CONFIG
  value: /data/scp/config.yaml
- name: OTEL_LOG_LEVEL
  value: info
{{- include "sisyphus.otelEnv" . }}
{{- end }}

{{- define "sisyphus.appEnvFrom" -}}
- secretRef:
    name: {{ include "sisyphus.secretName" . }}
{{- end }}

{{/* Config volume, mounted read-only by every sisyphus binary. */}}
{{- define "sisyphus.configVolume" -}}
- name: config
  configMap:
    name: {{ include "sisyphus.fullname" . }}-config
{{- end }}

{{- define "sisyphus.configMount" -}}
- name: config
  mountPath: /data/scp/config.yaml
  subPath: config.yaml
  readOnly: true
{{- end }}

{{- define "sisyphus.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end }}
