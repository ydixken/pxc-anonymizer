# The pointer contract

A pointer is a small JSON reference to an S3 backup prefix.
Publishing it does not copy, anonymize or verify every object in that backup.
Backup retention and restore credentials remain separate responsibilities.

## Schema v2

We retain `name` and `destination` from v1 and add optional publication metadata.
Publishers always encode `schemaVersion: 2`.
This example uses synthetic names and locations:

```json
{
  "name": "example-backup",
  "destination": "s3://example-backups/daily/example-backup-full",
  "schemaVersion": 2,
  "publishedAt": "2026-01-15T12:00:00Z",
  "publishedBy": {
    "kind": "BackupPointer",
    "namespace": "database-demo",
    "name": "latest-backup",
    "uid": "00000000-0000-0000-0000-000000000001"
  },
  "sourceCluster": {
    "name": "example-source",
    "namespace": "database-demo"
  },
  "backup": {
    "storageName": "daily",
    "completedAt": "2026-01-15T11:30:00Z",
    "state": "Succeeded"
  },
  "s3": {
    "bucket": "example-backups",
    "endpointUrl": "https://objects.example.com",
    "region": "auto"
  }
}
```

`destination` must identify a nonempty S3 bucket and object prefix without URL credentials, a query or a fragment.
The source backup's S3 location is distinct from the bucket and key storing the JSON document.
`s3.endpointUrl` uses that exact spelling on the wire; API configuration uses `endpointURL`.
Restore controllers can use the source endpoint and region as fallbacks when their restore configuration omits them.
The optional `anonymized` object identifies a Run, Policy and policy hash; optional `publicURL` describes the publication URL.
Neither field belongs in every publication.

Credentials never belong in a pointer.
Even a public document needs separate credentials to restore its private backup.
Use the [credential contract](../reference/prerequisites.md#credentials) for source system-user passwords and Percona's fixed S3 key names.

## Reading and compatibility

A document without `schemaVersion` decodes as version 1.
Unknown fields are ignored, and readers accept positive schema versions while validating the known fields.
Re-encoding a legacy document produces version 2; it does not invent missing publication metadata.

HTTP sources use a plain GET and require HTTPS unless `allowInsecure` explicitly permits HTTP.
Certificate verification remains enabled; a same-namespace CA Secret can supply additional trust.
Redirects are checked against the same URL rules.
The default timeout is 30 seconds and the default response limit is 65,536 bytes.
S3 sources use authenticated GET for private pointer buckets and the same 65,536-byte JSON limit.
HTTP reads require a successful 200 response; parsing valid JSON alone does not make another status successful.

Writes set `Content-Type: application/json` and `Cache-Control: no-cache`.
BackupPointer's periodic HEAD check verifies existence and the recorded ETag, not the continued availability of every backup object.
See [BackupPointer configuration](../configuration/backuppointer.md) for selection, missing-candidate and suspension behavior.

## Three distinct age checks

BackupPointer `Fresh` measures backup age from `backup.completedAt` through the resource's `staleAfter` setting.
`publishedAt` records when the document was uploaded, so republishing an old backup does not make that backup fresh.

A [Run](../configuration/run.md) using `backupPointerRef` requires a current-generation Ready condition and a recorded current backup.
Fresh=False emits a `StaleBackup` warning Event but does not itself reject that source.
Direct HTTP/S3 pointer sources do not gain an implicit age limit.

[Bootstrap](../configuration/bootstrap.md) optionally applies `pointer.maxAge` to `publishedAt`.
It rejects stale or future publication timestamps and rejects a v2 document without a publication timestamp when this check is enabled.
A v1 document without `publishedAt` remains accepted with a `LegacyPointerAgeUnknown` warning Event; its age could not be established.
A literal destination cannot be combined with `maxAge`, because it has no publication timestamp.

> [!important]
> A readable pointer and a recent publication timestamp do not prove a recent or restorable backup.
> Check the publisher's conditions, the backup retention policy and the consumer's age requirements separately.
