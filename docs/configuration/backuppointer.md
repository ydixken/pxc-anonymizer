# BackupPointer

`BackupPointer` publishes a JSON pointer to the latest successful Percona XtraDB Cluster backup in its namespace.
It selects existing backups; it does not create backups or anonymize their contents.

## Example

The [quickstart](../quickstart.md) creates this resource after checking the development context and supplying a credential Secret through your secret-management system.
The example names are illustrative.

```yaml
apiVersion: pxc-anonymizer.io/v1alpha1
kind: BackupPointer
metadata:
  name: latest-backup
  namespace: database-demo
spec:
  source:
    pxcCluster: example-source
    storageNames:
      - daily
  target:
    objectStorage:
      bucket: example-pointers
      credentialsSecretRef:
        name: pointer-credentials
    key: latest.json
  staleAfter: 36h
  verifyInterval: 1h
```

The Secret must contain the S3 access key, secret key and endpoint under the [shared object-storage keys](../reference/api.md#object-storage), unless those keys are remapped.
The bucket must exist and permit this identity to read and write the pointer object.
The selected backup's data can reside in a different bucket from the pointer.

## Spec

| Field | Contract |
| --- | --- |
| `source` | Required and immutable after creation. |
| `source.pxcCluster` | Required PXC cluster name in the same namespace. |
| `source.storageNames` | Optional set of Percona storage names; empty means any storage. |
| `source.selector` | Optional Kubernetes label selector for backup resources. |
| `target` | Required [PointerTarget](../reference/api.md#pointer-targets-and-sources). |
| `staleAfter` | Defaults to `36h`; freshness is measured from backup completion. |
| `verifyInterval` | Defaults to `1h`; interval for checking the published object. |
| `suspend` | Optional boolean; pauses reconciliation. |

Changing the source requires a new BackupPointer.
The target and timing fields remain mutable.

## Selection and publication

The controller filters backups by namespace, cluster, storage names and labels.
It chooses the latest `Succeeded` backup with an S3 destination, using completion time and falling back to creation time when completion is absent.
Newer Running or Starting backups do not block an already successful backup from being selected.

The pointer is written as JSON with `Content-Type: application/json`.
Its v2 contract preserves the legacy `name` and `destination` fields and adds publication metadata, including `schemaVersion: 2`.
The [pointer target](../reference/api.md#pointer-targets-and-sources) determines where the JSON is stored.
It does not move or copy the backup data.

If the selected backup disappears and no replacement qualifies, the controller reports the dangling pointer and leaves the object in storage.
Likewise, absence of a successful candidate does not delete an existing pointer.
Consumers must check pointer status and backup retention according to their needs.

## Status and conditions

`status.observedGeneration` identifies the spec generation processed by the controller.
`status.phase` is one of `Pending`, `Published`, `NoCandidate`, `Stale`, `Dangling`, `Error` or `Suspended`.

| Field | Meaning |
| --- | --- |
| `current` | [PublishedBackup](../reference/api.md#shared-status-shapes) represented by the pointer. |
| `previous` | Previous published backup record, when present. |
| `backups.succeeded` | Number of matching successful backups. |
| `backups.failed` | Number of matching failed backups. |
| `backups.running` | Number of matching running backups. |
| `backups.starting` | Number of matching starting backups. |
| `nextVerifyTime` | Next scheduled object verification. |

The condition types are `BackupSelected`, `Published`, `Fresh` and `Ready`.
`Ready` indicates successful selection and publication; `Fresh` independently checks the selected backup's age.
A pointer can therefore be Ready while Fresh is False.
Inspect the condition `reason` and `message` when a check fails.
The resource has short name `bp` and prints Cluster, Backup, Published, Ready, Fresh and Age columns.

1. Inspect a pointer after completing the quickstart's context selection.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   kubectl --context "$DEV_CONTEXT" -n database-demo get bp latest-backup
   kubectl --context "$DEV_CONTEXT" -n database-demo describe bp latest-backup
   ```
