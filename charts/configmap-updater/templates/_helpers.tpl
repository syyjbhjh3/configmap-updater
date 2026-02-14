{{- define "configmap-updater.fullname" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "configmap-updater.namespace" -}}
{{- default .Release.Namespace .Values.namespace -}}
{{- end -}}
