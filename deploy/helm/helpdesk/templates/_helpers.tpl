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
http://{{ include "helpdesk.fullname" . }}-database-agent:{{ .Values.agents.database.port }},http://{{ include "helpdesk.fullname" . }}-k8s-agent:{{ .Values.agents.k8s.port }},http://{{ include "helpdesk.fullname" . }}-sysadmin-agent:{{ .Values.agents.sysadmin.port }},http://{{ include "helpdesk.fullname" . }}-incident-agent:{{ .Values.agents.incident.port }},http://{{ include "helpdesk.fullname" . }}-research-agent:{{ .Values.agents.research.port }}
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
Log level environment variable. Include in every service pod.
*/}}
{{- define "helpdesk.logLevelEnv" -}}
{{- if .Values.logLevel }}
- name: HELPDESK_LOG_LEVEL
  value: {{ .Values.logLevel | quote }}
{{- end }}
{{- end -}}

{{/*
Identity environment variables — injected into agents and auditd so their
endpoints enforce the same auth as the gateway.
*/}}
{{- define "helpdesk.identityEnv" -}}
{{- if ne .Values.gateway.identity.provider "none" }}
- name: HELPDESK_IDENTITY_PROVIDER
  value: {{ .Values.gateway.identity.provider | quote }}
{{- end }}
{{- if or .Values.gateway.identity.usersConfig .Values.gateway.identity.usersSecret }}
- name: HELPDESK_USERS_FILE
  value: /etc/helpdesk/users.yaml
{{- end }}
{{- end -}}

{{/*
Users volume mount — include in volumeMounts when identity is configured.
*/}}
{{- define "helpdesk.usersVolumeMount" -}}
{{- if or .Values.gateway.identity.usersConfig .Values.gateway.identity.usersSecret }}
- name: users
  mountPath: /etc/helpdesk/users.yaml
  subPath: users.yaml
  readOnly: true
{{- end }}
{{- end -}}

{{/*
Users volume — include in volumes when identity is configured.
*/}}
{{- define "helpdesk.usersVolume" -}}
{{- if or .Values.gateway.identity.usersConfig .Values.gateway.identity.usersSecret }}
- name: users
  secret:
    secretName: {{ if .Values.gateway.identity.usersSecret }}{{ .Values.gateway.identity.usersSecret }}{{ else }}{{ include "helpdesk.fullname" . }}-users{{ end }}
    items:
      - key: users.yaml
        path: users.yaml
{{- end }}
{{- end -}}

{{/*
HELPDESK_AUDIT_API_KEY env block — emitted when a per-component secret is configured.
Usage: {{- include "helpdesk.auditAPIKeyEnv" (dict "secret" .Values.agents.database.auditAPIKeySecret "key" .Values.agents.database.auditAPIKeyKey) | nindent 12 }}
*/}}
{{- define "helpdesk.auditAPIKeyEnv" -}}
{{- if .secret }}
- name: HELPDESK_AUDIT_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .secret }}
      key: {{ .key | default "api-key" }}
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
