{{/* SPDX-License-Identifier: AGPL-3.0-only */}}

{{- define "ohs.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Release name, or release-chart when the release name does not hold the chart name. */}}
{{- define "ohs.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ohs.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ohs.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ohs.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "ohs.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ohs.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding OHS_DATABASE_URL and OHS_ENCRYPTION_KEY. */}}
{{- define "ohs.secretName" -}}
{{- if .Values.existingSecret -}}
{{- .Values.existingSecret -}}
{{- else -}}
{{- printf "%s-secret" (include "ohs.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* The component that serves HTTP: "api" with split roles, "server" otherwise. */}}
{{- define "ohs.httpComponent" -}}
{{- if .Values.splitRoles.enabled -}}api{{- else -}}server{{- end -}}
{{- end -}}

{{/* Name of the Deployment that serves HTTP. */}}
{{- define "ohs.httpDeployment" -}}
{{- if .Values.splitRoles.enabled -}}
{{- printf "%s-api" (include "ohs.fullname" .) -}}
{{- else -}}
{{- include "ohs.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "ohs.rawStoreURL" -}}
{{- if eq .Values.rawStore.type "s3" -}}
{{- required "rawStore.s3.url is required when rawStore.type is s3 (s3://bucket?region=...)" .Values.rawStore.s3.url -}}
{{- else if eq .Values.rawStore.type "file" -}}
file:///var/lib/ohs/raw
{{- else -}}
{{- fail (printf "rawStore.type must be file or s3, got %q" .Values.rawStore.type) -}}
{{- end -}}
{{- end -}}

{{/*
Fails the render on settings that cannot work. More than one pod needs a raw
store they all reach: s3, or a ReadWriteMany volume.
*/}}
{{- define "ohs.validate" -}}
{{- if and (not .Values.existingSecret) (not .Values.database.url) -}}
{{- fail "set database.url (postgres://user:password@host:5432/db?sslmode=require), or existingSecret" -}}
{{- end -}}
{{- $multi := or .Values.splitRoles.enabled .Values.autoscaling.enabled (gt (int .Values.replicaCount) 1) -}}
{{- $p := .Values.rawStore.persistence -}}
{{- if and $multi (eq .Values.rawStore.type "file") (not (and $p.enabled (eq $p.accessMode "ReadWriteMany"))) -}}
{{- fail "more than one pod (replicaCount > 1, autoscaling or splitRoles) needs a shared raw store: set rawStore.type=s3, or rawStore.persistence.accessMode=ReadWriteMany" -}}
{{- end -}}
{{- if and .Values.serviceMonitor.enabled (not .Values.config.metrics) -}}
{{- fail "serviceMonitor.enabled needs config.metrics=true" -}}
{{- end -}}
{{- end -}}

{{/* Whether the pods mount a single-writer volume (file store, not ReadWriteMany). */}}
{{- define "ohs.singleWriter" -}}
{{- $p := .Values.rawStore.persistence -}}
{{- if and (eq .Values.rawStore.type "file") $p.enabled (ne $p.accessMode "ReadWriteMany") -}}true{{- end -}}
{{- end -}}

{{- define "ohs.claimName" -}}
{{- .Values.rawStore.persistence.existingClaim | default (printf "%s-raw" (include "ohs.fullname" .)) -}}
{{- end -}}

{{/* Environment shared by every ohs container but the migration Job. */}}
{{- define "ohs.envFrom" -}}
- configMapRef:
    name: {{ include "ohs.fullname" . }}-config
- secretRef:
    name: {{ include "ohs.secretName" . }}
{{- with .Values.extraEnvFrom }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
One Deployment. Takes a dict: root (the chart context), name, component,
roles (OHS_ROLES), replicas, resources, http (serves the api role).
*/}}
{{- define "ohs.deployment" -}}
{{- $root := .root -}}
{{- $v := $root.Values -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  labels:
    {{- include "ohs.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  {{- if not (and .http $v.autoscaling.enabled) }}
  replicas: {{ .replicas }}
  {{- end }}
  revisionHistoryLimit: 5
  {{- if include "ohs.singleWriter" $root }}
  # The raw volume takes one writer: stop the old pod before the new one starts.
  strategy:
    type: Recreate
  {{- end }}
  selector:
    matchLabels:
      {{- include "ohs.selectorLabels" $root | nindent 6 }}
      app.kubernetes.io/component: {{ .component }}
  template:
    metadata:
      labels:
        {{- include "ohs.selectorLabels" $root | nindent 8 }}
        app.kubernetes.io/component: {{ .component }}
        {{- with $v.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        checksum/config: {{ include (print $root.Template.BasePath "/configmap.yaml") $root | sha256sum }}
        {{- if not $v.existingSecret }}
        checksum/secret: {{ dict "url" $v.database.url "key" $v.encryptionKey "env" $v.secretEnv "s3" $v.rawStore.s3 | toJson | sha256sum }}
        {{- end }}
        {{- with $v.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      {{- with $v.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.serviceAccountName }}
      serviceAccountName: {{ . }}
      {{- end }}
      automountServiceAccountToken: false
      {{- with $v.priorityClassName }}
      priorityClassName: {{ . }}
      {{- end }}
      securityContext:
        {{- toYaml $v.podSecurityContext | nindent 8 }}
      terminationGracePeriodSeconds: {{ $v.terminationGracePeriodSeconds }}
      containers:
        - name: ohs
          image: {{ include "ohs.image" $root }}
          imagePullPolicy: {{ $v.image.pullPolicy }}
          # Migrations run in their own Job (or by hand): the pods only serve.
          args: ["serve"]
          envFrom:
            {{- include "ohs.envFrom" $root | nindent 12 }}
          env:
            - name: OHS_ROLES
              value: {{ .roles | quote }}
            {{- with $v.env }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          {{- if .http }}
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          {{- with $v.startupProbe }}
          startupProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with $v.livenessProbe }}
          livenessProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with $v.readinessProbe }}
          readinessProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- end }}
          securityContext:
            {{- toYaml $v.securityContext | nindent 12 }}
          {{- with .resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
            {{- if eq $v.rawStore.type "file" }}
            - name: raw
              mountPath: /var/lib/ohs/raw
            {{- end }}
      volumes:
        - name: tmp
          emptyDir: {}
        {{- if eq $v.rawStore.type "file" }}
        - name: raw
          {{- if $v.rawStore.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "ohs.claimName" $root }}
          {{- else }}
          emptyDir: {}
          {{- end }}
        {{- end }}
      {{- with $v.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end -}}
