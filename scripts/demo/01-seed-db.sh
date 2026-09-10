#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'seed-db: %s\n' "$*" >&2; exit 1; }
context='' namespace='' host='' secret='' image='' scale=1
while (($#)); do
  case "$1" in
    --context|--namespace|--host|--secret|--image|--scale)
      [[ $# -ge 2 && -n $2 && $2 != --* ]] || fail "$1 requires a value"
      case "$1" in
        --context) context=$2 ;;
        --namespace) namespace=$2 ;;
        --host) host=$2 ;;
        --secret) secret=$2 ;;
        --image) image=$2 ;;
        --scale) scale=$2 ;;
      esac
      shift 2 ;;
    --help|-h)
      printf '%s\n' 'Usage: 01-seed-db.sh --context CONTEXT --namespace NAMESPACE' \
        '  --host MYSQL_HOST --secret ROOT_SECRET --image IMAGE [--scale 1..10]' \
        'Create one fresh seed-demo Job for a dedicated demo database and wait for completion.' \
        'The Secret must contain key root. This writes database data; existing Jobs are untouched.'
      exit 0 ;;
    *) fail "Unknown option; use --help" ;;
  esac
done
[[ -n $context && ! $context =~ [[:cntrl:]] ]] || fail "An explicit context without control characters is required"
[[ ${#namespace} -le 63 && $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "An explicit Kubernetes namespace is required"
[[ ${#secret} -le 253 && $secret =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || fail "Supply a valid root Secret name"
# Restrict interpolated scalars to their native forms so JSON needs no extra encoder.
[[ $host =~ ^[a-zA-Z0-9][a-zA-Z0-9.:-]*$ ]] || fail "Supply a database hostname or IP address"
[[ $image =~ ^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$ ]] || fail "Supply an image reference"
[[ $scale =~ ^([1-9]|10)$ ]] || fail "Scale must be an integer from 1 through 10"
command -v kubectl >/dev/null || fail "kubectl is required on PATH"

job=$(kubectl --context "$context" --namespace "$namespace" create --filename - --output name <<JSON
{
  "apiVersion": "batch/v1",
  "kind": "Job",
  "metadata": {"generateName": "demo-seed-", "namespace": "$namespace"},
  "spec": {
    "backoffLimit": 0,
    "activeDeadlineSeconds": 2100,
    "ttlSecondsAfterFinished": 3600,
    "template": {
      "spec": {
        "automountServiceAccountToken": false,
        "restartPolicy": "Never",
        "securityContext": {
          "runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
          "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}
        },
        "containers": [{
          "name": "seed", "image": "$image",
          "args": ["seed-demo", "--host=$host", "--port=3306", "--user=root",
            "--password-file=/var/run/secrets/pxc/root", "--wait=30m", "--scale=$scale"],
          "securityContext": {
            "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
            "capabilities": {"drop": ["ALL"]}
          },
          "volumeMounts": [{"name": "pxc-users", "mountPath": "/var/run/secrets/pxc", "readOnly": true}],
          "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"memory": "512Mi"}}
        }],
        "volumes": [{"name": "pxc-users", "secret": {
          "secretName": "$secret", "defaultMode": 256,
          "items": [{"key": "root", "path": "root"}]
        }}]
      }
    }
  }
}
JSON
)
[[ $job =~ ^job(\.batch)?/demo-seed-[a-z0-9]+$ ]] ||
  fail "Creation returned no expected Job identity; inspect the namespace before retrying"
printf 'Seed Job: %s\n' "$job"
# Allow the bounded Job to settle before the client observation window expires.
if ! kubectl --context "$context" --namespace "$namespace" wait "$job" --for=condition=Complete --timeout=36m; then
  fail "Seed completion was not confirmed; inspect the named Job before deciding on another attempt"
fi
