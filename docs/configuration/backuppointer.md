# BackupPointer

`BackupPointer` publishes a JSON reference to the latest successful Percona XtraDB Cluster backup in its namespace.
It selects existing backups without creating backups, copying their data or anonymizing their contents.

## Before creating a pointer

Follow the [quickstart](../quickstart.md) and supply the [storage prerequisites](../reference/prerequisites.md).
The source cluster and its backup resources must share the BackupPointer namespace.
The pointer bucket must already exist, and its credentials must permit reading and writing the configured object key.
The backup data can reside in a different bucket.

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

Use the [generated API reference](../reference/api.md) for field definitions and defaults.
The source is immutable; changing it requires a new BackupPointer.
The target, timing settings and suspension remain mutable.
Credential and CA references resolve in the resource namespace, with the pointer-specific key remapping described in [prerequisites](../reference/prerequisites.md#credentials).

## Selection and publication

Selection filters by namespace, source cluster, storage names and labels.
An empty storage-name list permits any storage.
The controller chooses a `Succeeded` S3 backup with a completion timestamp.
Successful backups without that timestamp are excluded; creation time is not a fallback.
A newer Running, Starting or failed backup does not block publication of an older successful backup.
The selection condition reports `NewerBackupInProgress` or `NewerBackupFailed` while remaining True.

Publication writes [schema-v2 JSON](../operations/pointer.md) with `Content-Type: application/json` and `Cache-Control: no-cache`.
At the verification interval, HEAD compares the object's ETag with the recorded publication.
A missing object or changed ETag causes another upload.
An upload records a new `publishedAt`; verification alone does not advance it.

If the selected backup disappears and no replacement qualifies, the controller marks the pointer dangling and leaves the stored object untouched.
No successful candidate, suspension and resource deletion also leave the stored pointer in place.
An available JSON document therefore does not prove that its backup still exists.

## Readiness and freshness

`Ready=True` means the selected successful backup is published.
`Fresh` separately compares its backup completion time with `staleAfter`, which defaults to `36h`.
A pointer can be Ready while Fresh is False.
`Published=True` can remain from an earlier publication while Ready is False, so check the complete condition set and its generations.

The [conditions reference](../reference/conditions.md#backuppointer) lists all reasons and a generation-aware wait procedure.
The [pointer contract](../operations/pointer.md) explains consumer behavior, publication age and legacy JSON compatibility.

1. Inspect the pointer in the development context confirmed during installation.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   kubectl --context "$DEV_CONTEXT" -n database-demo get bp latest-backup
   kubectl --context "$DEV_CONTEXT" -n database-demo describe bp latest-backup
   ```
