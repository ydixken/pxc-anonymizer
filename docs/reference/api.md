# API reference

All five kinds use `apiVersion: pxc-anonymizer.io/v1alpha1` and are namespaced.
The installed CRDs provide structural validation, defaults and CEL rules; they do not use admission webhooks.
Only `BackupPointer` has controller behavior in the M1 release.
Creating the other four kinds stores and validates their API contracts without running an anonymization, schedule or restore.

| Kind | Short name | Guide | M1 controller |
| --- | --- | --- | --- |
| `BackupPointer` | `bp` | [BackupPointer](../configuration/backuppointer.md) | Publishes pointers |
| `AnonymizationPolicy` | `apol` | [Policy](../configuration/policy.md) | Not implemented |
| `AnonymizationRun` | `arun` | [Run](../configuration/run.md) | Not implemented |
| `AnonymizationSchedule` | `asched` | [Schedule](../configuration/schedule.md) | Not implemented |
| `Bootstrap` | `bs` | [Bootstrap](../configuration/bootstrap.md) | Not implemented |

The [Go types](../../api/v1alpha1/) are the source of truth for the [generated CRDs](../../config/crd/bases/).
Each guide lists its spec fields, admission rules and status contract.
Status is a separate subresource; do not supply controller status as desired configuration.
Kubernetes conditions contain `type`, `status`, `reason`, `message`, `observedGeneration` and `lastTransitionTime`.
A condition that is absent does not establish success.

## Namespaces and references

Object references and credential references resolve in the custom resource's namespace.
Copy the required credentials through your secret-management system when resources occupy different namespaces.
Do not place credential values in custom resources, examples, command arguments or logs.
Admission validates a reference's shape; it does not prove that the referenced object or key exists.

## Object storage

`ObjectStorageSpec` is shared by pointer targets, S3 pointer sources and Run output.

| Field | Contract |
| --- | --- |
| `bucket` | Required; matches `^[a-z0-9.-]{3,63}$`. |
| `prefix` | Optional key prefix; cannot start with `/`. |
| `endpointURL` | Optional explicit S3 endpoint; overrides the Secret key. |
| `region` | Signing region; defaults to `auto`. |
| `credentialsSecretRef.name` | Optional same-namespace Secret reference. |
| `keys` | Optional Secret-key remapping, listed below. |
| `pathStyle` | Optional boolean; omission lets the client choose addressing style. |
| `tls.insecureSkipVerify` | Defaults to `false`. |
| `tls.caSecretRef` | Optional same-namespace Secret key containing a CA certificate. |

Admission requires `endpointURL` or `credentialsSecretRef`.
An endpoint alone does not provide credentials for authenticated S3 writes.
The storage client rejects endpoint paths, embedded credentials, query strings and fragments; these are runtime checks beyond the CRD shape.
TLS verification stays enabled unless explicitly disabled, and a custom CA can be supplied without disabling verification.
The default credential keys are `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.
The default endpoint keys are `S3_ENDPOINT_URL` and `S3_PUBLIC_ENDPOINT_URL`.
Override them through `keys.accessKeyID`, `keys.secretAccessKey`, `keys.endpointURL` and `keys.publicEndpointURL`.
A Secret key selector uses `name` and `key`, with Kubernetes' optional `optional` field.

## Pointer targets and sources

A `PointerTarget` contains required `objectStorage` and `key`, plus optional `publicURL`.
The key matches `^[A-Za-z0-9._/-]{1,512}$`.
`publicURL` is informational pointer metadata; setting it does not grant public bucket access.

A `PointerSource` requires exactly one of `http` or `s3`.
The S3 form contains required `objectStorage` and `key` with the same key pattern.
The HTTP form has these fields:

| Field | Contract |
| --- | --- |
| `url` | Required HTTPS URL. |
| `allowInsecure` | Defaults to `false`; explicit `true` also permits HTTP. |
| `caSecretRef` | Optional same-namespace CA Secret key. |
| `timeout` | Defaults to `30s`. |
| `maxBytes` | Response limit; defaults to `65536`. |

## Restore credentials and container arguments

`RestoreS3Credentials` contains required `credentialsSecret`, optional `endpointURL`, `region` defaulting to `auto`, and optional `verifyTLS`.
The restore endpoint contract falls back to the pointer's S3 endpoint when the explicit endpoint is omitted.
Restore controllers are not implemented in M1, so admission does not attempt that lookup.

`XtrabackupContainerOptions` contains optional argument arrays `xbcloud`, `xbstream` and `xtrabackup`.
Use these for non-secret backup or restore options.
Never supply credentials in argument arrays.

The [pointer package contract](../../internal/pointer/README.md) documents the v1/v2 codec and transport behavior.
Legacy JSON without `schemaVersion` decodes as version 1; encoding writes version 2.
S3 JSON reads and writes have a 65,536-byte limit.

## Shared status shapes

`PublishedBackup` contains `backupName`, `destination`, `storageName`, `completedAt`, `publishedAt`, `etag` and `schemaVersion`.
`ResolvedSource` contains `backupName`, `destination`, `sourceCluster`, `pointerPublishedAt` and `pointerSchemaVersion`.
Timestamps use Kubernetes date-time values; durations use strings such as `30m` or `3h`.
A duration field does not imply that admission checks every runtime constraint.

## Admission-only examples

The Policy, Run, Schedule and Bootstrap guides provide manifests for validating their contracts.
Use server-side dry-run with your explicitly selected development context to check a saved example without creating it.
Complete the context-selection step in [Installation](../installation.md) first.

1. Validate a manifest saved as `example.yaml`.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   kubectl --context "$DEV_CONTEXT" apply --dry-run=server -f example.yaml
   ```

Admission success for an inactive kind does not indicate that its controller or referenced services are available.
