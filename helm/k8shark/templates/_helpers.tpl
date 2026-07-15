{{- define "k8shark.labels" -}}
app.kubernetes.io/part-of: k8shark
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "k8shark.binaryImage" -}}
{{ .Values.image.registry }}/{{ .Values.image.binary }}:{{ .Values.image.tag }}
{{- end -}}

{{- define "k8shark.frontImage" -}}
{{ .Values.image.registry }}/{{ .Values.image.front }}:{{ .Values.image.tag }}
{{- end -}}
