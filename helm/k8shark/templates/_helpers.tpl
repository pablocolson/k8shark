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

{{/*
k8shark.imagePullPolicy: .Values.image.pullPolicy wins when explicitly set;
otherwise Always for a mutable "latest"/empty tag (so `helm upgrade` actually
re-pulls instead of a node's cached "latest" silently no-op'ing) and
IfNotPresent for a pinned immutable tag.
*/}}
{{- define "k8shark.imagePullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{ .Values.image.pullPolicy }}
{{- else if or (not .Values.image.tag) (eq .Values.image.tag "latest") -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}
