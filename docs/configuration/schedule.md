# AnonymizationSchedule

`AnonymizationSchedule` defines a cron schedule and a reusable Run template.
Its controller creates Runs for eligible cron ticks or manual requests and applies concurrency and history limits.
Start with the suspended [weekly Schedule example](../examples/06-schedule-weekly.yaml) after completing [anonymization planning](../planning.md).

## Admission example

This is an admission-only template using the same illustrative images and storage class as the [Run example](run.md#admission-example).

```yaml
apiVersion: pxc-anonymizer.io/v1alpha1
kind: AnonymizationSchedule
metadata:
  name: example-refresh
  namespace: database-demo
spec:
  schedule: "0 2 * * *"
  timeZone: UTC
  suspend: true
  template:
    metadata:
      labels:
        purpose: development
    spec:
      source:
        backupPointerRef:
          name: latest-backup
        restore:
          credentialsSecret: backup-credentials
          endpointURL: https://objects.example.com
      policyRef:
        name: example-policy
      tempCluster:
        crVersion: "1.20.0"
        image: percona/percona-xtradb-cluster:8.4.8-8.1
        backupImage: percona/percona-xtrabackup:8.4.0-5.1
        systemUsersSecretRef:
          name: source-system-users
        storage:
          storageClassName: development-storage
          size: 10Gi
      output:
        objectStorage:
          bucket: example-anonymized
          credentialsSecretRef:
            name: output-credentials
```

Use the [admission-only procedure](../installation.md#validate-a-manifest) to validate it.
The explicit suspension keeps the example inactive until its references are prepared and you enable it.

## Spec

| Field | Contract |
| --- | --- |
| `schedule` | Required nonempty cron string. |
| `timeZone` | IANA time-zone name; defaults to `UTC`. |
| `suspend` | Defaults to `false`. |
| `concurrencyPolicy` | `Forbid` by default, or `Allow` or `Replace`. |
| `startingDeadlineSeconds` | Optional nonnegative delay limit for missed runs. |
| `successfulRunsHistoryLimit` | Defaults to `3`; nonnegative, explicit zero is preserved. |
| `failedRunsHistoryLimit` | Defaults to `1`; nonnegative, explicit zero is preserved. |
| `template.metadata.labels` | Optional labels for generated Runs. |
| `template.metadata.annotations` | Optional annotations for generated Runs. |
| `template.spec` | Required complete [Run spec](run.md). |

Admission validates the cron string's presence, not cron syntax or time-zone validity.
The runtime contract accepts standard five-field cron and calendar descriptors such as `@hourly` or `@daily`.
It rejects `@every` intervals because Run names and missed-tick handling use calendar minutes.
Set the IANA time zone through `timeZone`; embedded `TZ=` or `CRON_TZ=` prefixes are rejected.
The embedded Run spec carries the same fields, defaults and validation as a standalone Run, including its immutable source, policy and temporary-cluster fields.
Do not depend on changing those fields in an existing template without checking admission.

The concurrency contract is to avoid overlap with `Forbid`, permit overlap with `Allow`, or replace an active Run with `Replace`.
Cleanup-pending and deleting Runs still count as active; Replace waits for actual cleanup and deletion before starting another Run.
Suspension also blocks manual run-now requests while retaining their pending token.
Generated Runs share the qualified `pxc-anonymizer.io/output-group` label so retention follows the Schedule's output lineage.
Long identities use a shared shortened label value; owner references and status retain their full identities.
History limits describe completed Run retention, not backup retention.

## Scheduling controller contract

After downtime, the controller selects the latest eligible missed tick using creation time, `lastScheduleTime` and `startingDeadlineSeconds`.
It creates at most one Run per reconciliation.
Retries preserve the same owned Run when creation succeeds but a status write fails, including under Forbid or Replace.
More than 100 missed ticks produces a `TooManyMissedTimes` Event and resets the cursor instead of creating a backlog.

The `pxc-anonymizer.io/run-now` annotation requests a manual Run and takes precedence over a scheduled tick.
Annotation presence is meaningful even for an empty token; use a distinct token for a new request.
The child name includes the first six hexadecimal characters of the token's SHA-256, and the full hash identifies the request on the child.
The annotation is removed only after the controller confirms its owned child; a newer concurrent token is preserved.
Forbid and suspension retain blocked manual requests for a later reconciliation.

Only strictly owned Runs are adopted into scheduling history or deleted by Replace and history pruning.
A foreign object with a colliding name is an error, not an object to adopt.
History pruning waits until successful or failed Runs have finished cleanup or entered explicit retention.

## Status contract

Status fields are `observedGeneration`, `conditions`, `active`, `activeCount`, `lastScheduleTime`, `lastSuccessfulTime`, `nextScheduleTime`, `lastRunName` and `lastRunPhase`.
`active` contains Run names; `activeCount` is the nonnegative integer used by the Active printer column.
`lastRunPhase` uses the Run phase enum.
The condition types are `ScheduleValid` and `Ready`; expected reasons include `Parsed`, `InvalidCron`, `Scheduled`, `Suspended` and `Blocked`.
The short name is `asched`, with Schedule, Suspend, Active, Last Run, Next and Age columns.

## Inspect and enable a Schedule

Prepare the Policy, source, same-namespace credentials, compatible images and storage class before enabling the Schedule.
Each new Run resolves its inputs and freezes its own execution snapshot; changing a referenced Policy does not rewrite an existing Run.

1. Select the development context confirmed during installation and inspect the adapted weekly example.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   SCHEDULE_NAMESPACE=database-demo
   SCHEDULE_NAME=example-weekly
   kubectl --context "$DEV_CONTEXT" -n "$SCHEDULE_NAMESPACE" \
     get anonymizationschedule "$SCHEDULE_NAME" -o yaml
   ```

2. Enable scheduling after reviewing the template and concurrency policy.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$SCHEDULE_NAMESPACE" \
     patch anonymizationschedule "$SCHEDULE_NAME" --type=merge \
     -p '{"spec":{"suspend":false}}'
   ```

3. Inspect the scheduling decision and active Runs.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$SCHEDULE_NAMESPACE" \
     get anonymizationschedule "$SCHEDULE_NAME" -o json |
     jq '{generation: .metadata.generation,
       observedGeneration: .status.observedGeneration,
       conditions: .status.conditions, active: .status.active,
       nextScheduleTime: .status.nextScheduleTime,
       lastRunName: .status.lastRunName, lastRunPhase: .status.lastRunPhase}'
   ```

Require a current-generation `Ready` condition when checking scheduling availability, using the [condition-checking procedure](../reference/conditions.md#generations-and-waits).
Ready does not prove that a child Run succeeded.
Inspect that Run's Complete, Failed and cleanup conditions separately.

## Suspend or request a manual Run

Suspension stops new cron and manual creation; it does not cancel already created Runs.
Resuming can make a missed tick eligible within `startingDeadlineSeconds`.
Choose that deadline when planning whether an old tick is still useful.

1. Suspend the selected Schedule when new Runs should stop.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$SCHEDULE_NAMESPACE" \
     patch anonymizationschedule "$SCHEDULE_NAME" --type=merge \
     -p '{"spec":{"suspend":true}}'
   ```

2. To request one manual Run, set a new token on the selected Schedule.

   ```sh
   REQUEST_TOKEN="manual-$(date -u +%Y%m%dT%H%M%SZ)"
   kubectl --context "$DEV_CONTEXT" -n "$SCHEDULE_NAMESPACE" \
     annotate anonymizationschedule "$SCHEDULE_NAME" \
     "pxc-anonymizer.io/run-now=$REQUEST_TOKEN" --overwrite
   ```

Use a distinct token for each intended request; do not issue two requests with the same timestamp token.
A suspended Schedule retains this request until enabled, and Forbid retains it while an active Run blocks it.
The annotation holds one pending token, not a queue of requests.
Follow the previous enabling and observation procedure when ready to execute it.
For GitOps-managed Schedules, keep the declared suspension state consistent with your change; see [Argo CD health checks](../operations/argocd.md).

## Concurrency and retention choices

Use Forbid when one temporary cluster at a time is sufficient.
Allow permits concurrent Runs, so budget storage for each active Run and its retained failures.
Replace requests deletion of the active Run and waits for its finalizer cleanup; it does not transfer an existing temporary cluster to the new Run.

Successful and failed history limits prune settled Run resources owned by the Schedule.
Deleting a retained failed Run releases its retained temporary cluster through normal cleanup.
Output backup retention instead comes from `template.spec.output.retention`, and pointer objects are not Run history entries.
Keep enough Run history for investigation without treating it as a backup-retention setting.
See [Retries, deadlines and deletion](../operations/lifecycle.md) for cleanup and output-retention boundaries.
