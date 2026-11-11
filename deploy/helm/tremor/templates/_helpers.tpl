{{- define "tremor.name" -}}tremor{{- end -}}

{{- define "tremor.fullname" -}}
{{- if contains "tremor" .Release.Name -}}{{ .Release.Name | trunc 50 | trimSuffix "-" }}{{- else -}}{{ printf "%s-tremor" .Release.Name | trunc 50 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "tremor.labels" -}}
app.kubernetes.io/name: tremor
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "tremor.selector" -}}
app.kubernetes.io/name: tremor
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "tremor.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "tremor.historyService" -}}{{ include "tremor.fullname" . }}-history{{- end -}}
