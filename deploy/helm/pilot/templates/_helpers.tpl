{{/*
Expand the name of the chart.
*/}}
{{- define "pilot.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "pilot.fullname" -}}
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
{{- define "pilot.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "pilot.labels" -}}
helm.sh/chart: {{ include "pilot.chart" . }}
{{ include "pilot.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "pilot.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pilot.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "pilot.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "pilot.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Return the image tag — defaults to Chart.appVersion.
*/}}
{{- define "pilot.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Return the full image reference.
*/}}
{{- define "pilot.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "pilot.imageTag" .) }}
{{- end }}

{{/*
Build the pilot start command arguments from values.
*/}}
{{- define "pilot.startArgs" -}}
{{- $args := list "start" }}
{{- if .Values.adapters.github.enabled }}
{{- $args = append $args "--github" }}
{{- end }}
{{- if .Values.adapters.telegram.enabled }}
{{- $args = append $args "--telegram" }}
{{- end }}
{{- if .Values.adapters.slack.enabled }}
{{- $args = append $args "--slack" }}
{{- end }}
{{- if .Values.adapters.linear.enabled }}
{{- $args = append $args "--linear" }}
{{- end }}
{{- $args = append $args (printf "--autopilot=%s" .Values.autopilot.mode) }}
{{- toJson $args }}
{{- end }}
