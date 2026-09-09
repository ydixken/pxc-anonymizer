# AnonymizationSchedule

`AnonymizationSchedule` defines a cron schedule and a reusable Run template.
M1 installs and validates the schema but does not implement the Schedule controller.
Creating a Schedule does not parse cron at runtime or create Runs.

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
The explicit suspension also keeps the example inactive when a scheduling controller is added later.

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
Those checks belong to the future controller.
The embedded Run spec carries the same fields, defaults and validation as a standalone Run, including its immutable source, policy and temporary-cluster fields.
Do not depend on changing those fields in an existing template without checking admission.

The concurrency contract is to avoid overlap with `Forbid`, permit overlap with `Allow`, or replace an active Run with `Replace`.
History limits describe completed Run retention, not backup retention.
No scheduling, replacement, history pruning or manual-trigger behavior runs in M1.

## Status contract

Status fields are `observedGeneration`, `conditions`, `active`, `activeCount`, `lastScheduleTime`, `lastSuccessfulTime`, `nextScheduleTime`, `lastRunName` and `lastRunPhase`.
`active` contains Run names; `activeCount` is the nonnegative integer used by the Active printer column.
`lastRunPhase` uses the Run phase enum.
The condition types are `ScheduleValid` and `Ready`; expected reasons include `Parsed`, `InvalidCron`, `Scheduled`, `Suspended` and `Blocked`.
M1 does not populate this status.
The short name is `asched`, with Schedule, Suspend, Active, Last Run, Next and Age columns.
