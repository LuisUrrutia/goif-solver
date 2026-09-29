#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
schema='https://raw.githubusercontent.com/yannh/kubernetes-json-schema/a6f9a32d2ccb64b6e4f5b41419b9c2e8ee0cce18/{{.NormalizedKubernetesVersion}}-standalone{{.StrictSuffix}}/{{.ResourceKind}}{{.KindSuffix}}.json'
artifacts/tools/kubeconform -strict -summary -kubernetes-version 1.37.0 \
  -schema-location "$schema" deploy/kubernetes.yaml deploy/redis.yaml
