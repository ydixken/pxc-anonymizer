{{/*
Chart name, truncated to the 63-char label limit.
*/}}
{{- define "pxc-anonymizer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. Skips the duplicated chart name when the release
name already contains it.
*/}}
{{- define "pxc-anonymizer.fullname" -}}
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
Chart name and version for the helm.sh/chart label.
*/}}
{{- define "pxc-anonymizer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "pxc-anonymizer.labels" -}}
helm.sh/chart: {{ include "pxc-anonymizer.chart" . }}
{{ include "pxc-anonymizer.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels: the immutable subset used by the Deployment selector.
*/}}
{{- define "pxc-anonymizer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pxc-anonymizer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name: created name, explicit name, or "default".
*/}}
{{- define "pxc-anonymizer.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "pxc-anonymizer.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
