{{/*
Fullname: release-name prefixed resource name.
*/}}
{{- define "helpdesk.fullname" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to all resources.
*/}}
{{- define "helpdesk.labels" -}}
app.kubernetes.io/name: helpdesk
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/*
Agent URL list for gateway and orchestrator environment variables.
*/}}
{{- define "helpdesk.agentURLs" -}}
http://{{ include "helpdesk.fullname" . }}-database-agent:{{ .Values.agents.database.port }},http://{{ include "helpdesk.fullname" . }}-k8s-agent:{{ .Values.agents.k8s.port }},http://{{ include "helpdesk.fullname" . }}-incident-agent:{{ .Values.agents.incident.port }},http://{{ include "helpdesk.fullname" . }}-research-agent:{{ .Values.agents.research.port }}
{{- end -}}

{{/*
Operating mode env var — emitted only when governance.operatingMode is set.
Include this in every process that calls EnforceFixMode or CheckFixModeAuditViolations.
*/}}
{{- define "helpdesk.operatingModeEnv" -}}
{{- if .Values.governance.operatingMode }}
- name: HELPDESK_OPERATING_MODE
  value: {{ .Values.governance.operatingMode | quote }}
{{- end }}
{{- end -}}

{{/*
Common model environment variables injected into every agent/orchestrator pod.
*/}}
{{- define "helpdesk.modelEnv" -}}
- name: HELPDESK_MODEL_VENDOR
  value: {{ .Values.model.vendor | quote }}
- name: HELPDESK_MODEL_NAME
  value: {{ .Values.model.name | quote }}
- name: HELPDESK_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.model.apiKeySecret }}
      key: {{ .Values.model.apiKeyKey }}
{{- end -}}
