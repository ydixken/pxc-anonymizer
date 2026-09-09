# AnonymizationSchedule

`AnonymizationSchedule` defines a cron schedule and a reusable Run template.
Its controller creates Runs for eligible cron ticks or manual requests and applies concurrency and history limits.

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

Use the [admission-only procedure](../reference/api.md#admission-only-examples) to validate it.
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
