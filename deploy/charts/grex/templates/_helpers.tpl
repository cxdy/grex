{{/*
Expand the name of the chart.
*/}}
{{- define "grex.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "grex.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "grex.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "grex.labels" -}}
helm.sh/chart: {{ include "grex.chart" . }}
{{ include "grex.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: grex
{{- end }}

{{/*
Selector labels
*/}}
{{- define "grex.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grex.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "grex.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "grex.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference for grex
*/}}
{{- define "grex.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
TLS cert file path inside the container
*/}}
{{- define "grex.tlsCertFile" -}}
{{- if .Values.tls.certFile }}
{{- .Values.tls.certFile }}
{{- else }}
{{- printf "%s/%s" (trimSuffix "/" .Values.tls.mountPath) .Values.tls.certKey }}
{{- end }}
{{- end }}

{{/*
TLS key file path inside the container
*/}}
{{- define "grex.tlsKeyFile" -}}
{{- if .Values.tls.keyFile }}
{{- .Values.tls.keyFile }}
{{- else }}
{{- printf "%s/%s" (trimSuffix "/" .Values.tls.mountPath) .Values.tls.keyKey }}
{{- end }}
{{- end }}

{{/*
TLS client CA file path inside the container (empty when client CA disabled)
*/}}
{{- define "grex.tlsClientCAFile" -}}
{{- if .Values.tls.clientCAFile }}
{{- .Values.tls.clientCAFile }}
{{- else if .Values.tls.clientCAKey }}
{{- printf "%s/%s" (trimSuffix "/" .Values.tls.mountPath) .Values.tls.clientCAKey }}
{{- end }}
{{- end }}

{{/*
OpAMP gateway fullname
*/}}
{{- define "grex.opampGateway.fullname" -}}
{{- printf "%s-opamp-gateway" (include "grex.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
OpAMP gateway selector labels
*/}}
{{- define "grex.opampGateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grex.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: opamp-gateway
{{- end }}

{{/*
OpAMP gateway labels
*/}}
{{- define "grex.opampGateway.labels" -}}
helm.sh/chart: {{ include "grex.chart" . }}
{{ include "grex.opampGateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: grex
{{- end }}

{{/*
In-cluster grex OpAMP URL for gateway upstream (scheme depends on TLS)
*/}}
{{- define "grex.opampEndpoint" -}}
{{- $host := printf "%s.%s.svc" (include "grex.fullname" .) .Release.Namespace }}
{{- $port := .Values.service.ports.opamp.port }}
{{- if .Values.tls.enabled }}
{{- printf "wss://%s:%v/v1/opamp" $host $port }}
{{- else }}
{{- printf "ws://%s:%v/v1/opamp" $host $port }}
{{- end }}
{{- end }}
