# Temporary cluster sizing and storage

Each [AnonymizationRun](../configuration/run.md) creates an isolated PXC cluster in its own namespace.
The controller restores the selected backup there before running the Policy.
It does not transform a separately managed database in place or adopt an unrelated cluster with the same name.

## Size before creating the Run

Set `spec.tempCluster.storage.storageClassName` explicitly and choose `storage.size` for each PXC replica.
The renderer limits the sum of those requested data volumes to 20Gi.
For example, one 10Gi replica requests 10Gi, while three 10Gi replicas exceed the limit.
The default replica count is 1.

This limit does not prove that a backup will fit or that the storage backend has enough free capacity.
Account for the restored database, transformation checkpoints and the storage backend's own replication when planning capacity.
The API does not inspect that capacity at admission.
Temporary-cluster settings are immutable after Run creation, so choose them before execution.

Supply PXC and XtraBackup images compatible with the source database and an installed Percona operator matching `crVersion`.
The generated [TempClusterSpec reference](../reference/api.md#tempclusterspec) lists the available settings.
Overrides remain subject to the renderer's identity, storage, security and cleanup constraints.

## Preserve the source system credentials

Set `tempCluster.systemUsersSecretRef` to a Secret in the Run's namespace containing the source cluster's system-user credentials.
The required keys are `root`, `xtrabackup`, `monitor`, `proxyadmin`, `operator` and `replication`.
Include the source backup encryption key when the backup requires it.
Restore brings back `mysql.user`, so newly generated replacement passwords would not match the restored database.

We keep resolved passwords, seeds, SQL and constant values in the Run's immutable Secret snapshot.
The policy ConfigMap and Run status do not contain those resolved Secret values.
Do not put credentials in a `configuration` fragment, an override or a manifest committed to Git.
See the [Run credential contract](../configuration/run.md#temporary-cluster) for the distinction between source system-user credentials and S3 restore credentials.

## Inspect the recorded cluster

Use `status.tempCluster.name` and `uid` rather than reconstructing names from the Run name.
The cluster name respects Percona's 22-byte limit, and Restore names also reserve space for Percona's derived Job labels.
An allocated name or `createdAt` timestamp is an intent record, not proof that a live cluster exists.

1. Select an existing Run in the development context confirmed during installation.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   RUN_NAMESPACE=database-demo
   RUN_NAME=example-run
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME"
   ```

2. Read the temporary-cluster identity once provisioning has recorded its name.

   ```sh
   TEMP_CLUSTER=$(kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" \
     get anonymizationrun "$RUN_NAME" -o jsonpath='{.status.tempCluster.name}')
   test -n "$TEMP_CLUSTER"
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" get anonymizationrun "$RUN_NAME" \
     -o jsonpath='{.status.tempCluster.uid}{"\n"}{.status.tempCluster.createdAt}{"\n"}{.status.tempCluster.readyAt}{"\n"}{.status.tempCluster.deletedAt}{"\n"}'
   ```

3. Inspect that named PXC resource and its owner identity.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$RUN_NAMESPACE" \
     get perconaxtradbcluster "$TEMP_CLUSTER" \
     -o jsonpath='{.metadata.uid}{"\n"}{.metadata.ownerReferences}{"\n"}{.status.state}{"\n"}{.metadata.deletionTimestamp}{"\n"}{.metadata.finalizers}{"\n"}'
   ```

The live cluster UID must match the recorded UID, and its controller owner must identify this Run's UID.
A missing recorded UID during provisioning is not permission to adopt another object.
A NotFound response after completed cleanup is expected; a name remaining in Run status is historical evidence.

## Hold, failure retention and deletion

Successful Runs can delay cleanup with `cleanup.holdTempClusterFor`; the default is `0s`.
`cleanup.onFailure: Retain` keeps the temporary cluster after failure until the Run is deleted.
The default failure policy is `Delete`.

> [!warning]
> A retained cluster can contain restored source data or only partly transformed data.
> Keep the same access restrictions used for the source backup while investigating it.

Normal Run deletion stops its active runner Job and waits for the owned PXC resource to finish its own finalizers.
Copied system-user credentials and known Percona-generated Secrets are removed only when their ownership matches the recorded Run or cluster identity.
Source credential Secrets and unowned output backups are preserved.
The Run's immutable snapshot Secret and policy ConfigMap remain owned by the Run until its deletion.
Follow the [deletion procedure](lifecycle.md#delete-through-the-run) instead of removing finalizers or deleting generated children separately.

If GitOps manages the Run, leave its generated PXC, Restore and Job resources under the controller's ownership.
Pruning a recorded Restore can cause `RestoreVanished`; the controller refuses to recreate an uncertain restore execution.
