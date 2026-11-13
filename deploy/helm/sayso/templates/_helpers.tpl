{{- define "sayso.name" -}}{{ .Release.Name }}{{- end -}}
{{- define "sayso.labels" -}}
app.kubernetes.io/name: sayso
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "sayso.image" -}}{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}{{- end -}}
{{- define "sayso.agentImage" -}}{{ .Values.agentImage.repository }}:{{ .Values.agentImage.tag | default .Chart.AppVersion }}{{- end -}}
{{- define "sayso.runnerNamespace" -}}{{ .Values.runner.namespace | default .Release.Namespace }}{{- end -}}
{{- define "sayso.env" -}}
- name: SAYSO_BASE_URL
  value: {{ .Values.baseURL | quote }}
- name: SAYSO_PUBLIC_WEBHOOK_URL
  value: {{ .Values.publicWebhookURL | quote }}
- name: SAYSO_INGRESS
  value: {{ .Values.ingressMode | quote }}
- name: SAYSO_BOOTSTRAP_ADMINS
  value: {{ .Values.bootstrapAdmins | quote }}
- name: SAYSO_TEMPORAL_ADDRESS
  value: {{ .Values.temporal.address | quote }}
- name: SAYSO_TEMPORAL_NAMESPACE
  value: {{ .Values.temporal.namespace | quote }}
{{- if .Values.temporal.tlsSecret }}
- name: SAYSO_TEMPORAL_TLS_CERT
  value: /etc/sayso/temporal-tls/tls.crt
- name: SAYSO_TEMPORAL_TLS_KEY
  value: /etc/sayso/temporal-tls/tls.key
{{- end }}
{{- if .Values.relay.enabled }}
- name: SAYSO_RELAY_URL
  value: http://{{ include "sayso.name" . }}-relay:{{ .Values.service.port }}
{{- end }}
{{- if .Values.temporalUi.enabled }}
- name: SAYSO_TEMPORAL_UI_URL
  value: http://{{ include "sayso.name" . }}-temporal-ui:8080
{{- end }}
- name: SAYSO_RUNNER
  value: kubernetes
- name: SAYSO_K8S_NAMESPACE
  value: {{ include "sayso.runnerNamespace" . | quote }}
- name: SAYSO_AGENT_IMAGE
  value: {{ include "sayso.agentImage" . | quote }}
- name: SAYSO_RUNNER_API_URL
  value: http://{{ include "sayso.name" . }}-api.{{ .Release.Namespace }}.svc:{{ .Values.service.port }}
{{- end -}}
{{- define "sayso.secretEnv" -}}
- secretRef:
    name: {{ .Values.existingSecret }}
{{- end -}}
{{- define "sayso.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: { drop: [ALL] }
{{- end -}}
