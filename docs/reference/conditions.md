# Conditions and reasons

Conditions describe observations, not just the next step in a pipeline.
Use their type, status, reason and observed generation for automation; messages are explanatory text.
An absent condition is not a successful check, and an earlier True condition can remain after a later stage fails.

## Generations and waits

Each condition carries `observedGeneration` for the spec generation it describes.
A condition update stamps that generation; `lastTransitionTime` changes when its status changes, not merely when its reason or message changes.
BackupPointer, Run, Schedule and Bootstrap also record `status.observedGeneration`.
Policy exposes the generation through its `Valid` condition, with no separate status-level field.
A status-level generation does not imply that every retained condition has been refreshed.

BackupPointer, Run and Bootstrap have a phase summary; Policy and Schedule do not.
Use the condition associated with the operation you need rather than treating every positive condition as mandatory.
Optional stages can be skipped, and terminal Runs preserve their completed execution rather than restarting after arbitrary edits.
For Bootstrap, also compare `status.observedTrigger` with the requested trigger when identifying an execution.

`kubectl wait` checks the selected condition's status.
The following procedure also rejects an absent or older-generation result and requires `jq`.

1. Select an existing resource in the development context confirmed during installation.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   RESOURCE=backuppointer/latest-backup
   CONDITION=Ready
   ```

2. Wait, then check that condition against the latest observed object generation.

   ```sh
   set -o pipefail
   kubectl --context "$DEV_CONTEXT" -n database-demo wait "$RESOURCE" \
     --for="condition=$CONDITION" --timeout=5m
   kubectl --context "$DEV_CONTEXT" -n database-demo get "$RESOURCE" -o json |
     jq -e --arg condition "$CONDITION" '
       .metadata.generation as $generation |
       any(.status.conditions[]?;
         .type == $condition and .status == "True" and .observedGeneration == $generation)
     '
   ```

Use `Valid` for Policy, `Ready` for Schedule and `Complete` for a successful Run or Bootstrap.
Check `Failed` while investigating an unsuccessful Run or Bootstrap; waiting only for Complete will not explain the failure.
For BackupPointer, check `Fresh` separately if backup age matters.

## BackupPointer

All four condition types have positive polarity.
Ready and Fresh are independent: an old backup can still be selected and published successfully.

| Condition | Status | Reason | Observation |
| --- | --- | --- | --- |
| BackupSelected | True | LatestSucceeded | Latest successful S3 backup selected. |
| BackupSelected | True | NewerBackupInProgress | Older success selected while a newer backup runs. |
| BackupSelected | True | NewerBackupFailed | Older success selected after a newer failure. |
| BackupSelected | False | NoSucceededBackup | No qualifying completed backup, or listing failed. |
| BackupSelected | False | SelectedBackupDeleted | Previous selection disappeared without a replacement. |
| Published | True | Uploaded | Pointer PUT succeeded. |
| Published | True | Verified | HEAD matched the recorded ETag. |
| Published | False | CredentialsUnavailable | Credential or CA Secret is unavailable. |
| Published | False | CredentialsInvalid | Credentials are incomplete or rejected. |
| Published | False | EndpointUnreachable | Endpoint is invalid, unreachable or timed out. |
| Published | False | UploadFailed | Other upload or verification failure. |
| Fresh | True | WithinStaleAfter | Published backup completion is within the age limit. |
| Fresh | False | StaleBackup | No published backup is within the age limit. |
| Ready | True | PointerCurrent | Selected successful backup is published. |
| Ready | False | PointerCurrent | Selection or publication is unavailable. |
| Ready | False | Dangling | Selected backup disappeared without a replacement. |
| Ready | False | Suspended | Reconciliation is suspended. |

Suspension updates Ready but can leave previous selection, publication and freshness conditions in place.
Neither dangling status nor deletion of the resource deletes the stored pointer.
See [BackupPointer configuration](../configuration/backuppointer.md).

## AnonymizationPolicy

The single `Valid` condition describes the current rules and required references.
Invalidation clears a previously computed policy hash.

| Condition | Status | Reason | Observation |
| --- | --- | --- | --- |
| Valid | True | SpecValid | Rules and referenced keys validated; hash recorded. |
| Valid | False | PatternInvalid | A pattern or other rule is invalid. |
| Valid | False | StepRefMissing | A step, referenced object or key is unavailable or could not be checked. |

Reference checks include SQL steps, fixed seeds and Secret-backed constants in the Policy namespace.
Secret changes are rechecked on the ten-minute refresh without Secret list/watch access.
See [Policy configuration](../configuration/policy.md).

## AnonymizationRun

A Run records each completed stage separately.
Failed=True marks an absorbing pipeline failure; cleanup or explicit failure retention must still settle.
A failure sets the affected stage False, Failed True and Complete False with the same reason.
A successful Run can omit Failed entirely.

| Condition | Status | Reason | Observation |
| --- | --- | --- | --- |
| SourceResolved | True | FromBackupPointer | Current-generation Ready BackupPointer selected. |
| SourceResolved | True | FromPointer | HTTP/S3 pointer decoded and snapshotted. |
| SourceResolved | True | FromBackup | Successful Percona backup selected. |
| SourceResolved | True | Literal | Explicit S3 destination selected. |
| SourceResolved | False | PointerNotReady | Referenced pointer is absent, unready or stale by generation. |
| SourceResolved | False | PointerFetchFailed | HTTP/S3 pointer could not be fetched. |
| SourceResolved | False | BackupNotSucceeded | Backup is absent or not successful. |
| SourceResolved | False | InvalidDestination | Destination, restore endpoint or credentials are invalid. |
| SourceResolved | False | Timeout | Source and snapshot resolution timed out. |
| PolicyValid | True | Snapshotted | Valid Policy and resolved payloads are frozen. |
| PolicyValid | False | PolicyNotFound | Referenced Policy is absent. |
| PolicyValid | False | PolicyInvalid | Policy, required payload or immutable snapshot is unusable. |
| TempClusterReady | False | Provisioning | Temporary cluster is being prepared. |
| TempClusterReady | True | Ready | Temporary cluster is ready. |
| TempClusterReady | False | ClusterError | Cluster identity, storage, credentials or configuration is invalid. |
| TempClusterReady | False | Timeout | Cluster readiness timed out. |
| Restored | False | Restoring | Source restore is underway. |
| Restored | True | Succeeded | Percona restore succeeded. |
| Restored | False | RestoreFailed | Restore configuration or execution failed. |
| Restored | False | RestoreVanished | Recorded restore is absent; replay is refused. |
| Restored | False | Timeout | Restore deadline elapsed. |
| Anonymized | False | Running | Runner is starting or executing. |
| Anonymized | True | Succeeded | Runner reported success. |
| Anonymized | True | DryRun | Runner completed without applying transformations. |
| Anonymized | False | Timeout | Runner attempt exceeded its deadline. |
| Anonymized | False | RunnerFailed | Runner outcome is invalid, ambiguous or unsafe to replay. |
| Anonymized | False | PolicyError | Runner configuration or policy execution failed. |
| Anonymized | False | SchemaMismatch | Runner reported a schema mismatch. |
| Anonymized | False | PermissionDenied | Runner reported insufficient permissions. |
| Anonymized | False | UniqueViolation | Runner exhausted a uniqueness constraint path. |
| Anonymized | False | RetryBudgetExhausted | Eligible transient retries exhausted their budget. |
| BackedUp | False | Running | Output backup is starting or running. |
| BackedUp | True | Succeeded | Output backup succeeded with a valid destination. |
| BackedUp | False | BackupFailed | Output backup identity, configuration or execution failed. |
| BackedUp | False | BackupDeadlineExceeded | Percona reported a backup deadline failure. |
| BackedUp | False | Timeout | Controller backup wait timed out. |
| Published | False | UploadFailed | Publication is pending, failed or exceeded its deadline. |
| Published | True | Uploaded | Output pointer was uploaded. |
| Published | True | Skipped | Publication is disabled or this is a dry run. |
| CleanedUp | False | Deleting | Owned temporary cluster deletion is still pending. |
| CleanedUp | True | TempClusterDeleted | Temporary cluster and owned credentials are absent. |
| Retained | True | Retained | Failure policy keeps the temporary cluster until Run deletion. |
| Complete | True | Succeeded | Successful pipeline and temporary cleanup finished. |
| Complete | False | Failing stage's reason | Pipeline failed; inspect Failed and cleanup state. |
| Failed | True | Failing stage's reason | Pipeline failed with the reason listed for that stage. |

Dry runs skip backup and publication; Complete=True does not require BackedUp=True in that case.
Failed Runs settle with either CleanedUp=True or Retained=True, and Schedule keeps them active until one of those conditions is present.
A cleanup deadline changes the explanation while deletion continues; it does not grant permission to abandon the owned resource.
A stale BackupPointer source emits a `StaleBackup` warning Event but remains usable when Ready is current.
See [Run configuration](../configuration/run.md) for retry, snapshot and cleanup contracts.

## AnonymizationSchedule

Ready describes scheduling availability, not the success of its latest Run.
The child Run carries its own pipeline conditions.

| Condition | Status | Reason | Observation |
| --- | --- | --- | --- |
| ScheduleValid | True | Parsed | Cron expression and time zone parsed. |
| ScheduleValid | False | InvalidCron | Schedule or time zone is unusable. |
| Ready | True | Scheduled | Waiting for a tick, or its Run exists or was created. |
| Ready | False | InvalidCron | Invalid schedule prevents creation. |
| Ready | False | Suspended | Cron and manual execution are suspended. |
| Ready | False | Blocked | Concurrency or pending deletion defers creation. |

A backlog above 100 missed ticks emits `TooManyMissedTimes` and resets the cursor without creating a Run; Ready remains True with reason Scheduled.
That warning is an Event reason, not a separate condition reason.
See [Schedule configuration](../configuration/schedule.md).

## Bootstrap

Bootstrap stages apply to one recorded trigger execution.
Optional Crossplane stages can be absent when disabled.
Their True status means that configured stage finished; it does not imply that every optional operation was enabled.

| Condition | Status | Reason | Observation |
| --- | --- | --- | --- |
| ClustersReady | False | WaitingForClusters | Waiting for target clusters. |
| ClustersReady | True | Ready | All recorded target clusters are ready. |
| PointerResolved | False | ResolvingPointer | Resolving the source and restore credentials. |
| PointerResolved | True | Resolved | Source and credentials resolved. |
| CrossplanePaused | False | Pausing | Selected pause stage is underway. |
| CrossplanePaused | True | Paused | Configured pause stage finished. |
| Restored | True | Restored | Every target restore and subsequent readiness check succeeded. |
| CrossplaneResumed | False | Resuming | Resuming owned pauses and checking readiness. |
| CrossplaneResumed | True | ReadyAndSynced | Expected resources are Ready and Synced. |
| CrossplaneRecreated | False | Recreating | Configured recreation is underway. |
| CrossplaneRecreated | True | Recreated | Configured recreation finished. |
| Complete | True | Completed | Restores and configured Crossplane stages finished. |
| Failed | False | Completed | Execution succeeded. |
| Complete | False | Failure reason below | Execution failed; compensation remains active. |
| Failed | True | Failure reason below | Execution failed with the recorded reason. |

Failure reasons identify the boundary that failed:

- `InvalidSpec`, `InvalidPhase`, `CheckpointMissing`, `SpecChanged`: execution configuration or durable state is unusable.
- `ClustersNotReady`, `TargetReplaced`, `TargetUnavailable`: a target failed its readiness or identity check.
- `PointerUnavailable`, `CredentialsUnavailable`, `InvalidRestoreConfiguration`: source or restore prerequisites failed.
- `RestoreTimeout`, `RestoreFailed`, `RestoreVanished`, `RestoreIdentityChanged`: a target restore failed, disappeared or changed identity.
- `CrossplanePauseTimeout`, `CrossplaneReadinessTimeout`: pause or Ready-and-Synced waiting exceeded its deadline.
- `CrossplaneRecreationTimeout`, `CrossplaneRecreationFailed`: recreation timed out or was rejected.

Failure does not mark each earlier stage False; inspect Failed and Complete alongside the retained stage history.
Compensation remains active after failure and during deletion, using recorded identities to avoid resuming a human-owned pause or repeating a missing restore.
A completed execution is not rerun for the same trigger.
Changing the trigger rearms it after compensation, preserving at most five history records.
`LegacyPointerAgeUnknown` and `ClusterUnpaused` are Event reasons rather than condition types.
See [Bootstrap configuration](../configuration/bootstrap.md) and the [pointer age contract](../operations/pointer.md#three-distinct-age-checks).
