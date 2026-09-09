# AnonymizationRun

`AnonymizationRun` defines one source, policy, temporary database and output configuration.
Its controller resolves and snapshots the inputs, restores a temporary PXC cluster, runs transformations, backs up the result and cleans up temporary resources.

## Admission example

The image names and storage class below are illustrative API values, not a tested restore recipe.
Select images compatible with your source database and an existing development storage class, then provision the referenced credentials before execution.

> [!warning]
> Apply this example only after replacing its illustrative references and checking development-cluster capacity.
> Creating a Run provisions a temporary database and executes the Policy's SQL and transformations there.

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

The controller checks pointer readiness and backup success; admission does not look up these resources.
`policyRef.name` identifies the Policy to validate and snapshot before creating runtime children.
The immutable Secret snapshot holds raw seed bytes, resolved SQL, constant values and source system-user passwords.
The policy ConfigMap holds canonical Policy JSON, frozen non-secret Run and source snapshots, and the policy hash.
The runner image and output group are resolved and frozen with those inputs.

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
| `systemUsersSecretRef.name` | Same-namespace source system-user Secret; optional in the schema, required by restore-backed execution. |
| `haproxy` | Defaults to `true`. |
| `configuration` | Optional `my.cnf` fragment. |
| `overrides` | Optional JSON merge patch; unknown fields are preserved. |
| `annotations`, `labels` | Optional metadata maps. |

Image compatibility, available capacity and the meaning of override fields are not verified by admission.
The runtime renderer requires an explicit storage class and limits total requested PXC data volume capacity across temporary replicas to 20Gi.
Cluster names must fit Percona's 22-byte limit.
Restore names reserve space for Percona's derived Job labels: valid short names stay unchanged, while longer candidates receive a shortened readable prefix and a deterministic hash suffix.
Inspect `status.restoreName` for the recorded child identity; retries preserve it.
HAProxy defaults to `percona/haproxy:2.8.18-1`; a deliberate spec override can supply a different compatible image.
The system-user Secret contract includes any backup encryption key needed to restore the source.
Supply `root`, `xtrabackup`, `monitor`, `proxyadmin`, `operator` and `replication` keys from the source cluster, because the restored backup contains its `mysql.user` credentials.
The runtime contract rejects a missing reference or required key before creating children; it neither generates replacement passwords nor discovers a source Secret automatically.
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
The output backup's credential Secret must use `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`, because Percona receives a Secret name rather than configurable credential-key names.
Nondefault `output.objectStorage.keys.accessKeyID` or `secretAccessKey` mappings are rejected before creating children.
This restriction applies to Percona output backups; pointer publication and direct object-storage access retain the [shared key-remapping support](../reference/api.md#object-storage).
The execution contract publishes the pointer before pruning older backups in the same output group.
Retention preserves backups from active or unsettled Runs and backups whose owner cannot be established, so concurrent Runs may temporarily retain more than the configured count.
Output backup resources are not owned by the Run and survive its deletion; temporary clusters and runner Jobs have a separate cleanup lifecycle.

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
`runner.image` overrides the manager's configured runner image; if neither is available, execution fails before creating a Job.
Runner preflight requires InnoDB for Anonymize targets, validates table keys and rejects primary-key mutation before any writes, including SQL pre-steps and checkpoint-schema creation.
Known fixed generated widths must fit the target column before writes, including the default hash widths of 64 hex or 44 base64 characters.
Table updates and their page checkpoint commit in one transaction; retrying an attempt does not reprocess committed rows.
The Run's creation timestamp supplies a fixed reference date for retries.
Dry-run performs validation without table changes, SQL-step writes or checkpoint-database creation.
Successful completion follows pointer publication, group retention, any requested hold and removal of the temporary cluster, its copied system-user Secret and owned Percona-generated Secrets.
The Run-owned seed Secret and policy ConfigMap retain the immutable snapshots until the Run is deleted.
Failed or deleting Runs stop their active Job before cleanup; successful Jobs follow the configured TTL.

## Runner invocation and results

The runtime image executes `/manager anonymize` with a canonical spec-only JSON file selected by `--policy`.
Password and seed inputs use `--password-file` and `--seed-file`; they are raw file contents, not command-line credentials.
The Job projects these inputs read-only with mode `0400` and supplies no Kubernetes API token.
It runs as UID 65532 with fsGroup 65532; kubelet grants the projected files effective group-read permission so the nonroot process can read them without a root init container.
`--steps-dir` contains `<step-name>.sql`, and `--constants-dir` contains `<secret-name>/<key>` for referenced constant values.
Sensitive resolved content stays outside the policy ConfigMap.

The command requires `--reference-time` as RFC3339 from the Run's immutable creation timestamp.
Its database TLS mode defaults to `--tls-mode=verify-full`, verifying the hostname with TLS 1.2 or newer and system trust roots.
`--tls-ca-file` adds a custom CA; the explicit `--tls-mode=disabled` option is used for temporary clusters configured without TLS.
A CA file is rejected when TLS is disabled.

Progress and SQL-step outcomes are bounded JSON log records.
The final verdict is JSON of at most 4 KiB in `--termination-file`, defaulting to `/dev/termination-log`, together with the exit code.
Reports omit raw SQL, connection strings and row values.

| Exit | Class |
| --- | --- |
| `0` | Success. |
| `1` | Transient failure. |
| `10` | Policy error. |
| `11` | Schema mismatch. |
| `12` | Permission failure. |
| `13` | Exhausted uniqueness retries. |

Automatic retry requires a valid report with `result: error`, `class: transient`, exit code 1 and the frozen policy hash.
Missing, malformed or ambiguous reports fail closed because successful anonymization may already have removed its checkpoint.
Errors while removing the checkpoint report Policy class with exit 10 and require recovery, because the server may have completed the removal before returning an error.
Retry state is persisted before deleting the previous Job.
Restore and runner Job names are persisted before their first Create call.
Recovery adopts a matching owned child; if the child is absent after recorded creation intent, the Run fails closed instead of repeating a restore or transformation.
Permission, policy, schema and uniqueness errors are terminal for that attempt sequence.
SQL-step uncertainty still requires the [explicit recovery described by Policy](policy.md#sql-references).

## Status contract

`status` contains `observedGeneration`, `conditions`, `source`, `policyHash`, `tempCluster`, `restoreName`, `anonymize`, `output`, `prunedBackups`, `startedAt` and `completedAt`.
`source` uses [ResolvedSource](../reference/api.md#shared-status-shapes), and `output` uses PublishedBackup.
The phase enum is `Pending`, `ResolvingSource`, `Provisioning`, `Restoring`, `Anonymizing`, `BackingUp`, `Publishing`, `Pruning`, `CleaningUp`, `Completed` or `Failed`.

`tempCluster` status contains `name`, `uid`, `secretName`, `createdAt`, `readyAt` and `deletedAt`.
The UID records the owned cluster identity before cleanup; known-name leftover Secrets also require matching owner identity before deletion.
`anonymize` contains `jobName`, `attempts`, `startedAt`, `completedAt`, `progress` and `lastResult`.
Progress fields are `tablesTotal`, `tablesDone`, `rowsTotal`, `rowsDone`, `currentTable`, `stepsDone` and `stepsTotal`; the last result contains `class` and `message`.
The condition contract covers `SourceResolved`, `PolicyValid`, `TempClusterReady`, `Restored`, `Anonymized`, `BackedUp`, `Published`, `CleanedUp`, `Complete` and `Failed`.
The short name is `arun`, with Phase, Source, Temp, Output, Complete and Age printer columns.
