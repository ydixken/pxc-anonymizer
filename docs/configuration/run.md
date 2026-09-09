# AnonymizationRun

`AnonymizationRun` defines one source, policy, temporary database and output configuration.
M1 installs and validates the schema but does not implement the Run controller or runner.
Creating a Run does not provision a database, restore a backup, anonymize data or publish an output.

## Admission example

The image names and storage class below are illustrative API values, not a tested restore recipe.
Select images compatible with your source database and an existing development storage class before implementing a runnable workflow.

```yaml
apiVersion: pxc-anonymizer.io/v1alpha1
kind: AnonymizationRun
metadata:
  name: example-run
  namespace: database-demo
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

## Source and immutable fields

`spec.source`, `spec.policyRef` and `spec.tempCluster` are required and immutable after creation.
The remaining required field is `spec.output`.
References resolve in the Run's namespace.

`source` requires exactly one of these forms, plus required [restore credentials](../reference/api.md#restore-credentials-and-container-arguments):

| Source field | Contract |
| --- | --- |
| `backupPointerRef.name` | BackupPointer whose current publication is selected. |
| `pointer` | Exactly one HTTP or S3 [PointerSource](../reference/api.md#pointer-targets-and-sources). |
| `backupRef.name` | Percona backup resource. |
| `destination` | Literal backup destination beginning with `s3://`. |

The future controller must check pointer readiness and backup success; admission does not look up these resources.
`policyRef.name` identifies the Policy to snapshot when execution is implemented.

## Temporary cluster

| Field under `tempCluster` | Contract |
| --- | --- |
| `namePrefix` | Defaults to `anon`; at most 20 characters. |
| `crVersion` | Required Percona API version. |
| `image` | Required PXC image compatible with the source. |
| `backupImage` | Required XtraBackup image. |
| `size` | Defaults to `1`. |
| `storage.storageClassName` | Required, nonempty storage class. |
| `storage.size` | Required Kubernetes storage quantity. |
| `resources` | Optional Kubernetes resource requirements. |
| `systemUsersSecretRef.name` | Optional source system-user Secret reference. |
| `haproxy` | Defaults to `true`. |
| `configuration` | Optional `my.cnf` fragment. |
| `overrides` | Optional JSON merge patch; unknown fields are preserved. |
| `annotations`, `labels` | Optional metadata maps. |

Image compatibility, available capacity and the meaning of override fields are not verified by admission.
The system-user Secret contract includes any backup encryption key needed to restore the source.
Do not embed credentials in `configuration` or `overrides`.

## Output

| Field under `output` | Contract |
| --- | --- |
| `objectStorage` | Required [object-storage configuration](../reference/api.md#object-storage). |
| `backupNamePrefix` | Optional prefix for output backup names. |
| `containerOptions` | Optional XtraBackup argument arrays. |
| `pointer` | Optional PointerTarget; omission disables publication in the execution contract. |
| `retention.keepSucceeded` | Defaults to `2`. |
| `retention.deleteFailedAfter` | Defaults to `24h`. |

The backup-name prefix has a controller default derived from the temporary cluster name; it is not a CRD default.
M1 does not create, retain, prune or delete output backups.

## Runner, timeouts and cleanup

| Field under `runner` | Contract |
| --- | --- |
| `image` | Optional runner-image override. |
| `resources` | Optional Kubernetes resource requirements. |
| `nodeSelector`, `tolerations`, `affinity` | Optional pod placement settings. |
| `workers` | Defaults to `4`; allowed range `1` to `32`. |
| `pageSize` | Defaults to `5000`. |
| `disableBinlog` | Defaults to `true`. |
| `dryRun` | Optional boolean. |

`timeouts` defaults are `clusterReady: 30m`, `restore: 3h`, `anonymize: 6h`, `backup: 3h`, `publish: 10m` and `cleanup: 30m`.
`cleanup.onFailure` accepts `Delete` or `Retain`, defaulting to `Delete`.
`cleanup.holdTempClusterFor` defaults to `0s` and defines retention after success.
`backoffLimit` defaults to `2` and applies to runner retries, not restore or backup retries.
`ttlSecondsAfterFinished` is optional and applies to owned Jobs only.
These are execution contracts for the later controller, not active M1 behavior.

## Status contract

`status` contains `observedGeneration`, `conditions`, `source`, `policyHash`, `tempCluster`, `restoreName`, `anonymize`, `output`, `prunedBackups`, `startedAt` and `completedAt`.
`source` uses [ResolvedSource](../reference/api.md#shared-status-shapes), and `output` uses PublishedBackup.
The phase enum is `Pending`, `ResolvingSource`, `Provisioning`, `Restoring`, `Anonymizing`, `BackingUp`, `Publishing`, `Pruning`, `CleaningUp`, `Completed` or `Failed`.

`tempCluster` status contains `name`, `secretName`, `createdAt`, `readyAt` and `deletedAt`.
`anonymize` contains `jobName`, `attempts`, `startedAt`, `completedAt`, `progress` and `lastResult`.
Progress fields are `tablesTotal`, `tablesDone`, `rowsTotal`, `rowsDone`, `currentTable`, `stepsDone` and `stepsTotal`; the last result contains `class` and `message`.
The condition contract covers `SourceResolved`, `PolicyValid`, `TempClusterReady`, `Restored`, `Anonymized`, `BackedUp`, `Published`, `CleanedUp`, `Complete` and `Failed`.
M1 does not populate Run status.
The short name is `arun`, with Phase, Source, Temp, Output, Complete and Age printer columns.
