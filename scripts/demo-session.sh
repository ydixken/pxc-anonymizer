#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/demo-session.sh --context CONTEXT \
  --source-namespace NAMESPACE --operator-namespace NAMESPACE \
  --downstream-namespace NAMESPACE [--session NAME] [--detach]

Open four read-only k9s panes natively in iTerm2 using tmux -CC.
Views: source PXC, Runs, pipeline events, Bootstrap.
The operator namespace must contain the Runs and their temporary resources.
The session defaults to pxc-demo. Reruns reuse a matching session.
--detach creates or checks the session without attaching to a terminal.
No cluster resources are created, changed or deleted by this launcher.
USAGE
}

fail() {
  printf 'demo-session: %s\n' "$*" >&2
  exit 1
}

context='' source_namespace='' operator_namespace='' downstream_namespace=''
session=pxc-demo
detach=false
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --detach) detach=true; shift; continue ;;
    --context|--source-namespace|--operator-namespace|--downstream-namespace|--session)
      [[ $# -ge 2 && -n $2 && $2 != --* ]] || fail "$1 requires a value"
      case "$1" in
        --context) context=$2 ;;
        --source-namespace) source_namespace=$2 ;;
        --operator-namespace) operator_namespace=$2 ;;
        --downstream-namespace) downstream_namespace=$2 ;;
        --session) session=$2 ;;
      esac
      shift 2
      ;;
    *) fail "Unknown option; use --help" ;;
  esac
done

[[ -n $context ]] || fail "--context is required"
# tmux treats a trailing semicolon as command syntax even in an argument array.
[[ $context != *';'* && ! $context =~ [[:cntrl:]] ]] ||
  fail "Context must not contain semicolons or control characters"
for namespace in "$source_namespace" "$operator_namespace" "$downstream_namespace"; do
  [[ ${#namespace} -le 63 && $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
    fail "All three namespaces are required and must be Kubernetes namespace names"
done
[[ ${#session} -le 64 && $session =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]] ||
  fail "Session name must be 1-64 letters, digits, underscores or hyphens"
command -v tmux >/dev/null || fail "tmux is required on PATH"
k9s_bin=$(command -v k9s) || fail "k9s is required on PATH"
if ! $detach; then
  [[ -t 0 && -t 1 ]] || fail "Attaching requires a terminal; use --detach for unattended setup"
  [[ ${TERM_PROGRAM:-} == iTerm.app ]] || fail "Interactive launch requires iTerm2; use --detach to prepare"
  [[ -z ${TMUX:-} ]] || fail "Launch from a regular iTerm2 tab outside tmux, or use --detach"
fi

target="=$session"
signature=$(printf '%q\n' "$context" "$source_namespace" "$operator_namespace" "$downstream_namespace")
if tmux has-session -t "$target" 2>/dev/null; then
  existing=$(tmux show-options -qv -t "$target:" @pxc-demo-config)
  [[ $existing == "$signature" ]] ||
    fail "Session already exists with different or incomplete settings; choose another --session"
else
  trap 'printf "demo-session: Setup failed; any created panes remain for inspection. Choose another --session after inspecting them.\n" >&2' ERR
  flags=(--readonly --context "$context" --logoless --splashless)
  source_pane=$(tmux new-session -d -P -F '#{pane_id}' -s "$session" -n demo -x 200 -y 60 \
    "$k9s_bin" "${flags[@]}" --namespace "$source_namespace" --command perconaxtradbclusterbackups \
    \; set-option -w -t "$target:demo" remain-on-exit on)
  tmux set-option -w -t "$target:demo" automatic-rename off
  tmux set-option -w -t "$target:demo" pane-border-status top
  tmux set-option -w -t "$target:demo" pane-border-format '#{@pxc-demo-title}'
  run_pane=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$source_pane" \
    "$k9s_bin" "${flags[@]}" --namespace "$operator_namespace" --command anonymizationruns)
  events_pane=$(tmux split-window -d -v -P -F '#{pane_id}' -t "$source_pane" \
    "$k9s_bin" "${flags[@]}" --namespace "$operator_namespace" --command events)
  downstream_pane=$(tmux split-window -d -v -P -F '#{pane_id}' -t "$run_pane" \
    "$k9s_bin" "${flags[@]}" --namespace "$downstream_namespace" --command bootstraps)
  tmux set-option -p -t "$source_pane" @pxc-demo-title '1 Source | PXC / backups / pointer'
  tmux set-option -p -t "$run_pane" @pxc-demo-title '2 Pipeline | Runs / temporary PXC'
  tmux set-option -p -t "$events_pane" @pxc-demo-title '3 Pipeline | events / jobs / PVCs'
  tmux set-option -p -t "$downstream_pane" @pxc-demo-title '4 Downstream | Bootstrap / PXC'
  tmux select-pane -t "$run_pane"
  dead_panes=$(tmux list-panes -t "$target:demo" -F '#{pane_dead}')
  [[ $dead_panes != *1* ]] || fail "A k9s pane exited during setup; inspect the retained pane"
  tmux set-option -t "$target:" @pxc-demo-config "$signature"
  trap - ERR
fi

if $detach; then
  printf 'Monitor session %s is available. From iTerm2 attach with: tmux -CC attach-session -t =%s\n' "$session" "$session"
else
  exec tmux -CC attach-session -t "$target"
fi
