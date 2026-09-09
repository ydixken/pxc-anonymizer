# Publish a BackupPointer

This workflow publishes a pointer to an existing successful PXC backup.
The pointer records the backup's location; creating and anonymizing backups are separate workflows.
Install the operator and complete the explicit development-context checks in [Installation](installation.md) first.

## Prepare storage and a backup

Use an existing development PXC namespace, cluster and S3-compatible bucket.
Provision the pointer credential Secret through your secret-management system in that namespace.
The Secret needs `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `S3_ENDPOINT_URL`, or the equivalent remapped keys in the [BackupPointer guide](configuration/backuppointer.md).
The S3 identity needs access to read and write the selected pointer object.
The bucket's access policy decides whether the result can be read over an unauthenticated public HTTPS URL.

1. Enter the existing namespace, PXC cluster, pointer bucket, signing region and credential Secret name.
   These are resource names, not credential values.

   ```sh
   : "${DEV_CONTEXT:?Complete the Installation development-context checks first}"
   printf 'Existing PXC namespace: '
   read -r BACKUP_NAMESPACE
   printf 'Existing PXC cluster name: '
   read -r PXC_CLUSTER
   printf 'Existing pointer bucket: '
   read -r POINTER_BUCKET
   printf 'S3 signing region (for example us-east-1): '
   read -r POINTER_REGION
   printf 'Existing pointer credential Secret name: '
   read -r POINTER_SECRET
   : "${BACKUP_NAMESPACE:?A namespace is required}"
   : "${PXC_CLUSTER:?A PXC cluster is required}"
   : "${POINTER_BUCKET:?A bucket is required}"
   : "${POINTER_REGION:?A signing region is required}"
   : "${POINTER_SECRET:?A credential Secret name is required}"
   export BACKUP_NAMESPACE PXC_CLUSTER POINTER_BUCKET POINTER_REGION POINTER_SECRET
   ```

2. Inspect the cluster, available backups and credential Secret metadata.
   Continue only when a backup for the chosen cluster is Succeeded, has a completion timestamp and has an S3 destination.
   This check prints no Secret data.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" \
     get perconaxtradbclusters.pxc.percona.com "$PXC_CLUSTER"
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" \
     get perconaxtradbclusterbackups.pxc.percona.com \
     -o 'custom-columns=NAME:.metadata.name,CLUSTER:.spec.pxcCluster,STATE:.status.state,COMPLETED:.status.completed,DESTINATION:.status.destination'
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" \
     get secret "$POINTER_SECRET" -o name
   ```

## Publish and inspect

1. Create `latest-backup` with a dedicated pointer key.
   The namespace and selected PXC cluster define the backup lineage; the source becomes immutable after creation.
   Reserve `latest.json` in this bucket for this publisher, because publication replaces that object.
   The [minimal example](examples/01-backuppointer-minimal.yaml) contains the same resource shape with illustrative names.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" apply -f - <<EOF
   apiVersion: pxc-anonymizer.io/v1alpha1
   kind: BackupPointer
   metadata:
     name: latest-backup
     namespace: ${BACKUP_NAMESPACE}
   spec:
     source:
       pxcCluster: ${PXC_CLUSTER}
     target:
       objectStorage:
         bucket: ${POINTER_BUCKET}
         region: ${POINTER_REGION}
         credentialsSecretRef:
           name: ${POINTER_SECRET}
       key: latest.json
     staleAfter: 36h
     verifyInterval: 1h
   EOF
   ```

2. Wait for publication and inspect both readiness and freshness.
   If the source backup completed more than 36 hours ago, Fresh is False even when publication succeeds.
   Confirm that the Ready condition describes the current generation rather than an earlier spec.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" \
     wait --for=condition=Ready backuppointer/latest-backup --timeout=180s
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" get bp latest-backup
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" get bp latest-backup -o json \
     | jq -e '.metadata.generation as $generation | any(.status.conditions[]?; .type == "Ready" and .status == "True" and .observedGeneration == $generation)'
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" \
     get bp latest-backup -o jsonpath='{.status.current.backupName}{"\n"}{.status.current.destination}{"\n"}'
   ```

3. Inspect any failed condition before retrying.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BACKUP_NAMESPACE" describe bp latest-backup
   ```

The selected name and destination identify the backup represented by the stored pointer.
The [BackupPointer guide](configuration/backuppointer.md) explains selection, freshness and dangling-pointer behavior.

## Read the pointer

The [pointer contract](operations/pointer.md) describes the v2 JSON and reader compatibility.
If the bucket already exposes this object through public HTTPS, read its URL without S3 credentials.
For a private bucket, use your existing authenticated S3 client to read `latest.json`; do not print or pass credentials as command arguments.
Setting a public URL in a BackupPointer does not change bucket access policy.

1. Read an existing public HTTPS pointer URL using curl and jq.

   ```sh
   printf 'Public HTTPS URL for latest.json: '
   read -r POINTER_URL
   case "$POINTER_URL" in
     https://*) ;;
     *) printf 'An HTTPS URL is required.\n' >&2; exit 1 ;;
   esac
   set -o pipefail
   curl --fail --silent --show-error "$POINTER_URL" \
     | jq -e -s 'select(length == 1 and (.[0] | .schemaVersion == 2 and (.name | length > 0) and (.destination | startswith("s3://")))) | .[0] | {name, destination}'
   ```

A successful read prints the validated `name` and `destination` as JSON.
Check that the JSON name and destination match `status.current` before using it for a restore.
To transform the selected backup, configure a [Policy](configuration/policy.md) and [Run](configuration/run.md), then use a [Schedule](configuration/schedule.md) for repeated refreshes.
A [Bootstrap](configuration/bootstrap.md) restores the published output into existing development targets.
