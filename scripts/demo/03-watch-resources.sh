#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'watch-resources: %s\n' "$*" >&2; exit 1; }
context='' namespace=''
while (($#)); do
  case "$1" in
    --context|--namespace)
      [[ $# -ge 2 && -n $2 && $2 != --* ]] || fail "$1 requires a value"
      if [[ $1 == --context ]]; then context=$2; else namespace=$2; fi
      shift 2 ;;
    --help|-h)
      printf '%s\n' 'Usage: 03-watch-resources.sh --context CONTEXT --namespace NAMESPACE RESOURCE [NAME]' \
        'Watch one resource kind, optionally one named object. Stop with Ctrl-C.'
      exit 0 ;;
    --) shift; break ;;
    -*) fail "Unknown option; use --help" ;;
    *) break ;;
  esac
done
[[ -n $context && ! $context =~ [[:cntrl:]] ]] || fail "An explicit context without control characters is required"
[[ ${#namespace} -le 63 && $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "An explicit Kubernetes namespace is required"
[[ $# -ge 1 && $# -le 2 && $1 =~ ^[a-z][a-z0-9.-]*$ ]] ||
  fail "Supply one resource kind, not a comma-separated collection or extra flags"
if [[ $# == 2 ]]; then
  [[ $2 =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || fail "Invalid resource name"
fi
command -v kubectl >/dev/null || fail "kubectl is required on PATH"
exec kubectl --context "$context" --namespace "$namespace" get "$@" --watch
