{{- define "fastagent.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "fastagent.labels" -}}
app.kubernetes.io/name: fastagent
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- /* DSN: prefer externalDSN, else fall back to bundled postgres. */ -}}
{{- define "fastagent.dsn" -}}
{{- if .Values.externalDSN -}}
{{ .Values.externalDSN }}
{{- else if .Values.postgres.enabled -}}
postgres://fastclaw:{{ required "postgres.password is required when postgres.enabled=true" .Values.postgres.password }}@{{ include "fastagent.fullname" . }}-db:5432/fastclaw?sslmode=disable
{{- else -}}
{{- fail "Either externalDSN or postgres.enabled must be set" -}}
{{- end -}}
{{- end -}}
