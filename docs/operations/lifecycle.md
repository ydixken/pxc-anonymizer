# Retries, deadlines and deletion

A Run executes one frozen source and Policy snapshot.
It proceeds through temporary-cluster provisioning, restore, anonymization, output backup, optional pointer publication, retention and cleanup.
An earlier successful condition remains useful evidence when a later stage fails.
Use the [condition reference](../reference/conditions.md#anonymizationrun) alongside the phase summary.

## Inspect an execution

These commands inspect one existing Run without reading its Secret payloads.

1. Select the Run in the development context confirmed during installation.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   RUN_NAMESPACE=database-demo
   RUN_NAME=example-run
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME"
   ```

2. Read its recorded stage results and child names.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME" \
     -o jsonpath='{.status.phase}{"\n"}{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.observedGeneration}{"\n"}{end}'
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME" \
     -o jsonpath='{.status.restoreName}{"\n"}{.status.anonymize.jobName}{"\n"}{.status.anonymize.attempts}{"\n"}{.status.output.backupName}{"\n"}'
   ```

3. Wait for successful completion when that is the expected outcome.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" wait anonymizationrun/"$RUN_NAME" \
     --for=condition=Complete --timeout=5m
   ```

The client wait is bounded independently of controller deadlines.
If it times out, inspect `Failed` and the current stage; timeout alone does not prove the Run failed.
Follow the [generation-aware check](../reference/conditions.md#generations-and-waits) when using conditions in automation.

`Complete=True` follows successful publication or an intentional skip, retention, any configured hold and temporary cleanup.
Dry-run skips backup and publication, so it can complete without `BackedUp=True`.
`Failed=True` is absorbing for the pipeline, but cleanup continues until `CleanedUp=True` or failure retention records `Retained=True`.
On failure, `status.completedAt` is the failure timestamp rather than proof that temporary resources are gone.

## Retry only a known transient result

`backoffLimit` defaults to 2 runner retries after the first attempt.
Restore and backup failures are not automatically retried as new executions.
Pointer upload can be retried within the publication deadline while preserving the previously stored pointer on failure.

An automatic runner retry requires a valid termination report with all of these facts:

- `result: error` and `class: transient`;
- exit code 1;
- the frozen Policy hash for this Run.

The controller persists that retry decision before deleting the old Job and creating the next attempt.
The immutable snapshot and fixed reference time are reused.
Committed page updates and their checkpoints are not reapplied by a normal retry.

Missing, malformed or ambiguous reports fail closed.
The runner may already have completed its work and removed its checkpoint, so absence of a report is not permission to replay it.
Permission, Policy, schema and uniqueness errors are absorbing for the attempt sequence.
An uncertain checkpoint removal is also absorbing.
For uncertain SQL-step execution, follow the [Policy recovery contract](../configuration/policy.md#sql-references).

Restore and runner Job creation intent is persisted before the first Create call.
Recovery can use an existing child only when its identity and ownership match the recorded execution.
If the recorded child is absent, the Run fails instead of repeating a restore or transformation.
Do not create a replacement child manually to bypass `RestoreVanished` or `RunnerFailed`.

Correct the underlying input or infrastructure problem before creating a new Run with a new identity.
Edits to a failed or completed Run do not rearm it, and changing mutable spec fields does not replace its frozen execution snapshot.

## Controller deadlines

| Field under `spec.timeouts` | Default | Applies to |
| --- | --- | --- |
| `clusterReady` | `30m` | Source resolution and temporary-cluster readiness waits. |
| `restore` | `3h` | The recorded Percona restore. |
| `anonymize` | `6h` | Each runner attempt. |
| `backup` | `3h` | The output backup. |
| `publish` | `10m` | Pointer publication. |
| `cleanup` | `30m` | The cleanup wait before reporting its exceeded deadline. |

Source resolution and provisioning use separate start timestamps with the same `clusterReady` limit; it is not a deadline for the whole Run.
A deadline does not grant permission to abandon owned resources.
If PXC finalizers still block cluster deletion after the cleanup deadline, the Run retains its cleanup finalizer and continues waiting.
Inspect the named cluster's state and finalizers using the [temporary-cluster guide](temp-cluster.md#inspect-the-recorded-cluster).

## Retention boundaries

`cleanup.holdTempClusterFor` delays successful cleanup; `cleanup.onFailure: Retain` keeps a failed temporary cluster for inspection.
Choose these settings before execution because cleanup uses the frozen Run configuration.
Failed or deleting Runs stop the active runner Job before handling the temporary cluster.
`ttlSecondsAfterFinished` applies to owned Jobs, not to the Run or its output backups.

Output retention runs after pointer publication or an intentional publication skip.
It works within the frozen output group, keeps the current output, and defaults to retaining two successful backups and removing failed backups older than 24h.
Dry-run skips output backup and retention as well as publication.
Backups from active or unsettled Runs and backups without trustworthy owner evidence are preserved, so the configured count is not a strict concurrent upper bound.
Output backup resources have no Run owner reference and survive Run deletion.
See [output configuration](../configuration/run.md#output) for the separate S3 credential contract.

## Delete through the Run

Use normal Run deletion after inspecting any retained data and recording the output identity you need.
If GitOps manages the Run, remove its desired manifest through the same delivery workflow so reconciliation does not create it again.

> [!warning]
> Deleting a Run ends failure retention and requests deletion of its temporary database.
> Do not strip the Run or PXC finalizers to bypass unfinished cleanup.

1. Confirm the named Run and its output before deletion.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   : "${RUN_NAMESPACE:?Set RUN_NAMESPACE to the Run namespace}"
   : "${RUN_NAME:?Set RUN_NAME to the Run name}"
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME" \
     -o jsonpath='{.metadata.uid}{"\n"}{.status.phase}{"\n"}{.status.tempCluster.name}{"\n"}{.status.output.backupName}{"\n"}'
   ```

2. Request ordinary deletion and wait for cleanup within a bounded client timeout.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" delete anonymizationrun "$RUN_NAME" --wait=false
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" wait anonymizationrun/"$RUN_NAME" \
     --for=delete --timeout=35m
   ```

If the wait expires, inspect the remaining finalizers and cleanup condition rather than repeating deletion with force options.
Our finalizer is removed only after owned cleanup finishes; another finalizer can still keep the Run object present.
Run metrics are forgotten when our deletion finalizer is gone, including that case.
The stored output pointer and unowned output backup are not deleted by this procedure.
