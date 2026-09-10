#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

stage=preflight
trap 'printf "demo-pipeline: %s: command failed; no mutation is retried\n" "$stage" >&2' ERR
fail() { printf 'demo-pipeline: %s: %s\n' "$stage" "$*" >&2; exit 1; }
context='' source_ns='' operator_ns='' downstream_ns='' backup_file='' bootstrap_file=''
pointer_name='' schedule_name='' timeout=7200
while (($#)); do
  case "$1" in
    --help|-h)
      printf '%s\n' 'Usage: 02-apply-manifests.sh --context CONTEXT --source-namespace NS --operator-namespace NS' \
        '  --downstream-namespace NS --source-backup FILE.json --source-pointer NAME' \
        '  --schedule NAME --bootstrap FILE.json [--timeout-seconds 7200]' \
        'Create a fresh source backup, trigger the existing Schedule, and restore its output through Bootstrap.' \
        'This changes cluster resources. Each wait is bounded; failures never retry a mutation.'
      exit 0 ;;
    --context|--source-namespace|--operator-namespace|--downstream-namespace|--source-backup|\
    --source-pointer|--schedule|--bootstrap|--timeout-seconds)
      [[ $# -ge 2 && -n $2 && $2 != --* && ! $2 =~ [[:cntrl:]] ]] || fail "$1 requires a value"
      case "$1" in
        --context) context=$2 ;; --source-namespace) source_ns=$2 ;;
        --operator-namespace) operator_ns=$2 ;; --downstream-namespace) downstream_ns=$2 ;;
        --source-backup) backup_file=$2 ;; --source-pointer) pointer_name=$2 ;;
        --schedule) schedule_name=$2 ;; --bootstrap) bootstrap_file=$2 ;; --timeout-seconds) timeout=$2 ;;
      esac
      shift 2 ;;
    *) fail 'Unknown argument; use --help' ;;
  esac
done
[[ -n $context ]] || fail 'An explicit context is required'
for name in "$source_ns" "$operator_ns" "$downstream_ns" "$pointer_name" "$schedule_name"; do
  [[ ${#name} -le 63 && $name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || fail 'Explicit valid namespaces/names are required'
done
[[ $timeout =~ ^[1-9][0-9]*$ && ${#timeout} -le 5 ]] || fail 'Timeout must be 1..99999 seconds'
for tool in kubectl jq shasum mktemp; do command -v "$tool" >/dev/null || fail "$tool is required on PATH"; done
for file in "$backup_file" "$bootstrap_file"; do
  [[ -f $file && -r $file && -s $file ]] || fail 'Both manifests must be readable, nonempty JSON files'
done
backup_template=$(jq -ce . < "$backup_file") || fail 'Invalid source backup JSON'
bootstrap_template=$(jq -ce . < "$bootstrap_file") || fail 'Invalid Bootstrap JSON'

# jq variables are interpreted by jq, not the shell.
# shellcheck disable=SC2016
jq_defs='def present: type == "string" and length > 0;
def current($type): . as $o | any(.status.conditions[]?;
  .type == $type and .status == "True" and .observedGeneration == $o.metadata.generation);
def second: if type == "string" then sub("\\.[0-9]+Z$"; "Z") else . end;
def refs: map({kind, name, uid}) | sort_by(.kind,.name);'
check() { jq -e "$jq_defs $1" >/dev/null; }
field() { jq -er "$1 | select(type == \"string\" and length > 0)"; }
k() { local ns=$1; shift; kubectl --context "$context" --namespace "$ns" "$@" --request-timeout=30s; }
get() { k "$1" get "$2" "$3" -o json; }
record() {
  local next
  next=$(jq --arg key "$1" --argjson value "$2" '.[$key]=$value' "$evidence")
  printf '%s\n' "$next" > "$evidence"
}
pause_wait() { ((SECONDS < deadline)) || fail 'Timed out waiting for positive completion evidence'; sleep 2; }
observe() {
  local phase
  phase=$(jq -r '.status.phase // .status.state // "AwaitingStatus"' <<< "$object")
  if [[ $phase != "$last_phase" ]]; then printf '%s: observed %s\n' "$stage" "$phase"; last_phase=$phase; fi
  [[ $phase != Failed && $phase != Error && $phase != error ]] || fail "Observed failure phase $phase"
  check 'current("Failed") | not' <<< "$object" || fail 'Observed current-generation Failed condition'
}
identity() {
  jq -e --arg uid "$1" '.metadata.uid == $uid and .metadata.deletionTimestamp == null' <<< "$object" >/dev/null ||
    fail 'Resource disappeared, changed UID, or is deleting'
}

check '.apiVersion == "pxc.percona.com/v1" and .kind == "PerconaXtraDBClusterBackup" and
  (.metadata.generateName | present) and .metadata.name == null and .metadata.uid == null and
  .metadata.ownerReferences == null and .status == null and (.spec.pxcCluster | present) and
  (.spec.storageName | present)' <<< "$backup_template" || fail 'Expected a fresh generateName Percona backup template'
[[ $(field '.metadata.namespace' <<< "$backup_template") == "$source_ns" ]] || fail 'Backup namespace mismatch'
check '.apiVersion == "pxc-anonymizer.io/v1alpha1" and .kind == "Bootstrap" and
  (.metadata.name | present) and .metadata.uid == null and .metadata.ownerReferences == null and .status == null and
  (.spec.pointer.http.url | present) and (.spec.targets | length) == 1 and
  .spec.crossplane.pause == true and .spec.crossplane.resume == true and .spec.crossplane.recreate == null and
  (.spec.crossplane.selector.matchLabels | length) > 0 and
  (.spec.crossplane.selector.matchExpressions // [] | length) == 0 and
  (.metadata.annotations["pxc-anonymizer.io/demo-crossplane-count"] | tonumber) > 0' <<< "$bootstrap_template" ||
  fail 'Expected one Bootstrap target with explicit Crossplane pause/resume, selection and expected count'
[[ $(field '.metadata.namespace' <<< "$bootstrap_template") == "$downstream_ns" ]] || fail 'Bootstrap namespace mismatch'
bootstrap_name=$(field '.metadata.name' <<< "$bootstrap_template")
target_name=$(field '.spec.targets[0].pxcCluster' <<< "$bootstrap_template")
output_url=$(field '.spec.pointer.http.url' <<< "$bootstrap_template")
source_cluster=$(field '.spec.pxcCluster' <<< "$backup_template")
source_storage=$(field '.spec.storageName' <<< "$backup_template")
cp_group=$(field '.spec.crossplane.group' <<< "$bootstrap_template")
cp_kinds=$(jq -er --arg group "$cp_group" '.spec.crossplane.kinds | select(length > 0) | map(.+"."+$group) | join(",")' <<< "$bootstrap_template")
cp_selector=$(jq -er '.spec.crossplane.selector.matchLabels | to_entries | map(.key+"="+.value) | join(",")' <<< "$bootstrap_template")
cp_count=$(jq -er '.metadata.annotations["pxc-anonymizer.io/demo-crossplane-count"] | tonumber' <<< "$bootstrap_template")
provenance=pxc-anonymizer-demo-pipeline

check_idle() {
  local runs bootstraps
  runs=$(k "$operator_ns" get anonymizationruns.pxc-anonymizer.io -o json)
  check '(.items | type) == "array" and all(.items[];
    .metadata.deletionTimestamp == null and .status.phase == "Completed" and current("Complete"))' <<< "$runs" ||
    fail 'A Run is active, failed, deleting, or lacks current Complete evidence'
  bootstraps=$(k "$downstream_ns" get bootstraps.pxc-anonymizer.io -o json)
  check '(.items | type) == "array" and all(.items[];
    .metadata.deletionTimestamp == null and .status.phase == "Completed" and current("Complete") and
    .status.observedGeneration == .metadata.generation and .status.observedTrigger == .spec.trigger)' <<< "$bootstraps" ||
    fail 'A downstream Bootstrap is active, failed, deleting, or not current'
}
check_bootstrap() {
  existing=$(k "$downstream_ns" get bootstraps.pxc-anonymizer.io "$bootstrap_name" --ignore-not-found -o json)
  [[ -n $existing ]] || return 0
  jq -e --arg owner "$provenance" --argjson want "$bootstrap_template" "$jq_defs
    .metadata.labels[\"app.kubernetes.io/managed-by\"] == \$owner and
    (.metadata.ownerReferences // [] | length) == 0 and .metadata.deletionTimestamp == null and
    .spec.pointer == \$want.spec.pointer and .spec.targets == \$want.spec.targets and
    .spec.restore == \$want.spec.restore and .spec.crossplane == \$want.spec.crossplane and
    .status.phase == \"Completed\" and current(\"Complete\") and
    .status.observedGeneration == .metadata.generation and .status.observedTrigger == .spec.trigger and
    (.status.execution.id | present)" <<< "$existing" >/dev/null ||
    fail 'Existing Bootstrap is foreign, changed, or not safely complete'
}
check_crossplane() {
  cp=$(k "$downstream_ns" get "$cp_kinds" --selector "$cp_selector" -o json)
  jq -e --argjson count "$cp_count" '.items | length == $count and all(.[];
    (.metadata.uid | type == "string" and length > 0) and .metadata.deletionTimestamp == null and
    .metadata.annotations["crossplane.io/paused"] != "true" and
    .metadata.annotations["pxc-anonymizer.io/bootstrap-pause-owner"] == null and
    any(.status.conditions[]?; .type == "Ready" and .status == "True") and
    any(.status.conditions[]?; .type == "Synced" and .status == "True"))' <<< "$cp" >/dev/null ||
    fail 'Selected Crossplane resources are missing, paused, deleting or not Ready/Synced'
  cp_refs=$(jq -c '.items | map({kind:(.kind | ascii_downcase)+"s",name:.metadata.name,uid:.metadata.uid}) | sort_by(.kind,.name)' <<< "$cp")
}
check_target() {
  target=$(get "$downstream_ns" perconaxtradbclusters.pxc.percona.com "$target_name")
  check '.metadata.deletionTimestamp == null and .spec.pause != true and .status.state == "ready" and
    (.metadata.uid | present)' <<< "$target" || fail 'Downstream PXC is missing, paused, deleting or not ready'
}
check_schedule() {
  schedule=$(get "$operator_ns" anonymizationschedules.pxc-anonymizer.io "$schedule_name")
  jq -e --arg url "$output_url" "$jq_defs
    (.metadata.uid | present) and .metadata.deletionTimestamp == null and .spec.suspend == false and
    .spec.concurrencyPolicy == \"Forbid\" and current(\"Ready\") and
    .metadata.annotations[\"pxc-anonymizer.io/run-now\"] == null and
    (.status.active // [] | length) == 0 and .spec.template.spec.output.pointer.publicURL == \$url" <<< "$schedule" >/dev/null ||
    fail 'Schedule is not ready/Forbid, has a pending request, or publishes to a different pointer'
}
check_idle
check_bootstrap
check_schedule
schedule_uid=$(field '.metadata.uid' <<< "$schedule")
check_target
target_uid=$(field '.metadata.uid' <<< "$target")
check_crossplane
original_cp=$cp_refs
pointer=$(get "$source_ns" backuppointers.pxc-anonymizer.io "$pointer_name")
jq -e --arg cluster "$source_cluster" --arg storage "$source_storage" '
  .spec.source.pxcCluster == $cluster and (.spec.source.storageNames | index($storage)) != null' <<< "$pointer" >/dev/null ||
  fail 'BackupPointer does not select the requested source cluster/storage'
pointer_uid=$(field '.metadata.uid' <<< "$pointer")
evidence=$(mktemp "${TMPDIR:-/tmp}/pxc-talk-pipeline.XXXXXX")
printf '{}\n' > "$evidence"
printf 'Private identity record: %s\n' "$evidence"

stage="source backup in $source_ns"
printf '%s: creating a fresh backup\n' "$stage"
object=$(k "$source_ns" create -f - -o json <<< "$backup_template")
backup_name=$(field '.metadata.name' <<< "$object")
backup_uid=$(field '.metadata.uid' <<< "$object")
field '.metadata.creationTimestamp' <<< "$object" >/dev/null
record sourceBackup "$(jq -c '{name:.metadata.name,uid:.metadata.uid,createdAt:.metadata.creationTimestamp}' <<< "$object")"
stage="source backup $source_ns/$backup_name"
last_phase='' deadline=$((SECONDS+timeout))
while :; do
  object=$(get "$source_ns" perconaxtradbclusterbackups.pxc.percona.com "$backup_name")
  identity "$backup_uid"; observe
  jq -e --arg cluster "$source_cluster" --arg storage "$source_storage" '
    .spec.pxcCluster == $cluster and .spec.storageName == $storage' <<< "$object" >/dev/null ||
    fail 'Source backup cluster/storage changed' 
  if check '.status.state == "Succeeded" and (.status.destination | present) and (.status.completed | present)' <<< "$object"; then break; fi
  pause_wait
done
source_destination=$(field '.status.destination' <<< "$object")
source_completed=$(field '.status.completed' <<< "$object")
record sourceBackup "$(jq -c '{name:.metadata.name,uid:.metadata.uid,createdAt:.metadata.creationTimestamp,destination:.status.destination,completedAt:.status.completed}' <<< "$object")"
stage="BackupPointer $source_ns/$pointer_name"
printf '%s: waiting for publication of %s\n' "$stage" "$backup_name"
deadline=$((SECONDS+timeout))
while :; do
  object=$(get "$source_ns" backuppointers.pxc-anonymizer.io "$pointer_name"); identity "$pointer_uid"
  if jq -e --arg name "$backup_name" --arg destination "$source_destination" --arg completed "$source_completed" "$jq_defs
    current(\"Ready\") and current(\"Fresh\") and .status.current.backupName == \$name and
    .status.current.destination == \$destination and (.status.current.completedAt | second) == (\$completed | second) and
    (.status.current.publishedAt | present)" <<< "$object" >/dev/null; then break; fi
  pause_wait
done
source_pointer=$(jq -c '.status.current' <<< "$object")
record sourcePointer "$source_pointer"
printf '%s: published the fresh source backup\n' "$stage"

stage="Schedule $operator_ns/$schedule_name"
check_idle; check_schedule
[[ $(field '.metadata.uid' <<< "$schedule") == "$schedule_uid" ]] || fail 'Schedule UID changed'
token=$backup_uid
token_hash=$(printf '%s' "$token" | shasum -a 256); token_hash=${token_hash%% *}
record schedule "$(jq -cn --arg name "$schedule_name" --arg uid "$schedule_uid" --arg token "$token" --arg hash "$token_hash" '{name:$name,uid:$uid,token:$token,tokenHash:$hash}')"
printf '%s: requesting one Run for source backup %s\n' "$stage" "$backup_name"
k "$operator_ns" annotate anonymizationschedules.pxc-anonymizer.io "$schedule_name" \
  "pxc-anonymizer.io/run-now=$token" --resource-version "$(field '.metadata.resourceVersion' <<< "$schedule")" >/dev/null
printf '%s: waiting for the exact owned Run\n' "$stage"
deadline=$((SECONDS+timeout))
while :; do
  runs=$(k "$operator_ns" get anonymizationruns.pxc-anonymizer.io -o json)
  matches=$(jq -c --arg hash "$token_hash" '[.items[] | select(.metadata.annotations["pxc-anonymizer.io/run-now-token-hash"] == $hash)]' <<< "$runs")
  count=$(jq 'length' <<< "$matches")
  ((count <= 1)) || fail 'More than one Run carries this token hash'
  if ((count == 1)); then
    object=$(jq -c '.[0]' <<< "$matches")
    jq -e --arg uid "$schedule_uid" --arg name "$schedule_name" --arg ns "$operator_ns" '
      .metadata.namespace == $ns and .metadata.deletionTimestamp == null and
      .metadata.labels["pxc-anonymizer.io/schedule"] == $name and
      any(.metadata.ownerReferences[]?; .controller == true and .kind == "AnonymizationSchedule" and
        (.apiVersion | startswith("pxc-anonymizer.io/")) and .name == $name and .uid == $uid)' <<< "$object" >/dev/null ||
      fail 'Token matched a foreign or deleting Run'
    break
  fi
  pause_wait
done
run_name=$(field '.metadata.name' <<< "$object")
run_uid=$(field '.metadata.uid' <<< "$object")
record run "$(jq -c '{name:.metadata.name,uid:.metadata.uid}' <<< "$object")"
stage="Run $operator_ns/$run_name"
last_phase='' deadline=$((SECONDS+timeout))
while :; do
  object=$(get "$operator_ns" anonymizationruns.pxc-anonymizer.io "$run_name"); identity "$run_uid"; observe
  if check '.status.source != null' <<< "$object"; then
    jq -e --arg name "$backup_name" --arg destination "$source_destination" --arg cluster "$source_cluster" --argjson pointer "$source_pointer" "$jq_defs
      .status.source.backupName == \$name and .status.source.destination == \$destination and
      .status.source.sourceCluster == \$cluster and .status.source.pointerSchemaVersion == \$pointer.schemaVersion and
      (.status.source.pointerPublishedAt | second) == (\$pointer.publishedAt | second)" <<< "$object" >/dev/null || fail 'Run resolved a different source backup'
  fi
  if check '.status.phase == "Completed" and .status.observedGeneration == .metadata.generation and
    current("Complete") and current("Anonymized") and
    current("BackedUp") and current("Published")' <<< "$object"; then break; fi
  pause_wait
done
run=$object
check '(.status.source.backupName | present) and (.status.tempCluster.name | present) and
  (.status.output.backupName | present) and (.status.output.destination | present) and
  (.status.output.completedAt | present) and (.status.output.publishedAt | present) and
  (.status.output.etag | present) and (.status.output.schemaVersion | type == "number" and . > 0)' <<< "$run" ||
  fail 'Completed Run lacks source/output publication evidence'
output=$(jq -c '.status.output' <<< "$run")
output_name=$(field '.backupName' <<< "$output")
record output "$output"
printf '%s: Complete, anonymized and published output %s\n' "$stage" "$output_name"
check_output_backup() {
  output_backup=$(get "$operator_ns" perconaxtradbclusterbackups.pxc.percona.com "$output_name")
  jq -e --argjson output "$output" --arg cluster "$(field '.status.tempCluster.name' <<< "$run")" "$jq_defs .status.state == \"Succeeded\" and
    (.metadata.uid | present) and .metadata.deletionTimestamp == null and
    .metadata.name == \$output.backupName and .spec.pxcCluster == \$cluster and
    .spec.storageName == \$output.storageName and .status.destination == \$output.destination and
    (.status.completed | second) == (\$output.completedAt | second)" <<< "$output_backup" >/dev/null ||
    fail 'Output backup identity or successful completion does not match the Run'
}
check_output_backup
output_uid=$(field '.metadata.uid' <<< "$output_backup")
record outputBackup "$(jq -c '{name:.metadata.name,uid:.metadata.uid}' <<< "$output_backup")"

stage="Bootstrap $downstream_ns/$bootstrap_name"
check_idle; check_bootstrap; check_target; check_crossplane
[[ $(field '.metadata.uid' <<< "$target") == "$target_uid" && $cp_refs == "$original_cp" ]] ||
  fail 'Downstream target or selected Crossplane identities changed'
old_execution=''
printf '%s: starting restore of output %s; the controller will pause and resume %s Crossplane resources\n' \
  "$stage" "$output_name" "$cp_count"
if [[ -z $existing ]]; then
  payload=$(jq -c --arg owner "$provenance" --arg trigger "$run_uid" \
    '.metadata.labels["app.kubernetes.io/managed-by"]=$owner | .spec.trigger=$trigger' <<< "$bootstrap_template")
  object=$(k "$downstream_ns" create -f - -o json <<< "$payload")
else
  old_execution=$(field '.status.execution.id' <<< "$existing")
  patch=$(jq -c --arg trigger "$run_uid" '[
    {op:"test",path:"/metadata/uid",value:.metadata.uid},
    {op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
    {op:"add",path:"/spec/trigger",value:$trigger}]' <<< "$existing")
  object=$(k "$downstream_ns" patch bootstraps.pxc-anonymizer.io "$bootstrap_name" --type=json -p "$patch" -o json)
fi
bootstrap_uid=$(field '.metadata.uid' <<< "$object")
bootstrap_generation=$(jq -er '.metadata.generation | select(type == "number" and . > 0)' <<< "$object")
record bootstrap "$(jq -c '{name:.metadata.name,uid:.metadata.uid,generation:.metadata.generation,trigger:.spec.trigger}' <<< "$object")"
last_phase='' deadline=$((SECONDS+timeout))
while :; do
  object=$(get "$downstream_ns" bootstraps.pxc-anonymizer.io "$bootstrap_name"); identity "$bootstrap_uid"
  jq -e --arg trigger "$run_uid" --argjson generation "$bootstrap_generation" '
    .spec.trigger == $trigger and .metadata.generation == $generation' <<< "$object" >/dev/null ||
    fail 'Bootstrap trigger or generation changed during this execution'
  if jq -e --arg trigger "$run_uid" '.status.observedTrigger == $trigger' <<< "$object" >/dev/null; then
    observe
    if check '.status.source != null' <<< "$object"; then
      jq -e --argjson output "$output" --arg cluster "$(field '.status.tempCluster.name' <<< "$run")" "$jq_defs
        .status.source.backupName == \$output.backupName and .status.source.destination == \$output.destination and
        .status.source.sourceCluster == \$cluster and .status.source.pointerSchemaVersion == \$output.schemaVersion and
        (.status.source.pointerPublishedAt | second) == (\$output.publishedAt | second)" <<< "$object" >/dev/null ||
        fail 'Bootstrap resolved a different published output'
    fi
    if check '.status.phase == "Completed" and current("Complete") and
      .status.observedGeneration == .metadata.generation' <<< "$object"; then break; fi
  fi
  pause_wait
done
jq -e --arg prior "$old_execution" --arg target "$target_uid" --argjson selected "$original_cp" "$jq_defs
  (.status.execution.id | present) and .status.execution.id != \$prior and
  (.status.source.backupName | present) and .status.execution.targets[0].uid == \$target and
  (.status.execution.targets[0].restoreUID | present) and
  (.status.execution.selected | refs) == \$selected and (.status.crossplane.paused | refs) == \$selected and
  (.status.crossplane.resumed | refs) == \$selected and (.status.crossplane.pending // [] | length) == 0 and
  (.status.crossplane.recreating // [] | length) == 0 and (.status.crossplane.recreated // [] | length) == 0" <<< "$object" >/dev/null ||
  fail 'Complete Bootstrap lacks exact target/restore/Crossplane execution evidence'
record bootstrapResult "$(jq -c '{phase:.status.phase,trigger:.status.observedTrigger,executionID:.status.execution.id,source:.status.source,selected:.status.execution.selected}' <<< "$object")"
check_target; check_crossplane; check_output_backup
[[ $(field '.metadata.uid' <<< "$target") == "$target_uid" && $cp_refs == "$original_cp" &&
   $(field '.metadata.uid' <<< "$output_backup") == "$output_uid" ]] || fail 'A retained output/downstream identity changed'
object=$(get "$operator_ns" anonymizationruns.pxc-anonymizer.io "$run_name"); identity "$run_uid"
[[ $(jq -c '.status.output' <<< "$object") == "$output" ]] || fail 'Run output changed during downstream restore'
printf '%s: Complete for this Run; downstream ready, %s original Crossplane resources resumed\n' "$stage" "$cp_count"
printf 'Controller lifecycle complete. Application-user login verification is separate. Evidence: %s\n' "$evidence"
