# Prerequisites

Install the [operator](../installation.md) after preparing the dependencies needed by the resources you intend to use.
The chart installs the five pxc-anonymizer CRDs; it does not install Percona, Crossplane, databases or object storage.
Use the [generated API reference](api.md) for field definitions and the configuration guides for execution requirements.

## Percona and storage

Install the Percona XtraDB Cluster operator and its cluster, backup and restore CRDs before starting the manager.
The repository's Percona API contract and vendored schemas are pinned to `1.20.0`.
Choose PXC, XtraBackup and HAProxy images compatible with the source backup; admission does not establish that compatibility.

For BackupPointer, provide a source cluster and successful S3 backup resources with completion timestamps.
For Runs, provide capacity and an explicit storage class for a temporary PXC cluster.
The runtime limits the total requested PXC data volume capacity across temporary replicas to 20Gi.
For Bootstrap, prepare existing target clusters and confirm that replacing their contents is intended.
See the [Run](../configuration/run.md) and [Bootstrap](../configuration/bootstrap.md) guides before creating either resource.

Create pointer and backup buckets before use.
Give each credential access to its intended bucket and operations: pointer publication needs PUT and HEAD, private pointer consumption needs GET, and Percona backup/restore uses its own S3 credentials.
Configure an absolute S3 endpoint origin without a path, URL credentials, query or fragment.
An unset addressing style uses SDK selection; `pathStyle: true` selects path-style access.
Supply the region required by the service rather than assuming every provider accepts the default `auto`.

## Credentials

All Secret and ConfigMap references resolve in the custom resource's namespace.
Provision them through your secret-management system and keep credentials out of manifests, logs and pointer documents.
Kubernetes Secret data is already decoded when the controller reads it; do not encode the stored password or access key a second time.

| Use | Required convention |
| --- | --- |
| Pointer S3 access | Default keys `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`; optional remapping. |
| Endpoint fallback | Default key `S3_ENDPOINT_URL`; explicit configuration takes precedence. |
| Percona backup and restore | Secret keys `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. |
| Source system users | Explicit same-namespace Secret for a restore-backed Run. |

Pointer publication and direct object-storage access support the API's key remapping.
Percona receives a Secret name rather than configurable key names, so Run output backups reject nondefault access-key or secret-key mappings before creating children.
Use separate explicit restore credentials for the source backup.

The Run's `tempCluster.systemUsersSecretRef` must contain the source values for `root`, `xtrabackup`, `monitor`, `proxyadmin`, `operator` and `replication`.
The restored backup includes `mysql.user`, so newly generated replacement passwords would not authenticate to it.
The controller neither generates those passwords nor discovers a source Secret automatically.
Include any backup encryption key required by the source.

## Namespace scope and permissions

An empty chart `watchNamespaces` list watches namespaced resources across the cluster.
A nonempty list becomes the manager's `--watch-namespaces` value and narrows its informer cache.
It does not narrow the chart's ClusterRoleBinding or the uncached API reader's authorization.
The chart has no namespaced-permissions switch.

The generated manager ClusterRole grants the following runtime access:

| Resource family | Access |
| --- | --- |
| Five pxc-anonymizer kinds | Read/watch; status updates; controller-specific lifecycle writes. |
| Percona clusters and restores | Read/watch, create, patch and delete. |
| Percona backups | Read/watch, create and delete. |
| ConfigMaps and Jobs | Read/watch, create and delete. |
| Secrets | Get, create and delete only. |
| Pods and pod logs | Read/watch Pods; get logs. |
| Crossplane MySQL Database/User/Grant | Get, list, patch and delete. |
| Controller Events | Create and patch. |

The manager does not list or watch Secrets.
Policy Secret changes are detected through its ten-minute refresh, while referenced ConfigMap changes can enqueue validation.
Leader election has a separate Role and RoleBinding in the release namespace when enabled.
If `rbac.create` is disabled, supply the required permissions separately; disabling generation does not reduce the controller's requirements.

## Crossplane

Crossplane is needed only when Bootstrap's Crossplane integration is configured.
Install the provider and its managed-resource CRDs, configure working ProviderConfigs, and select the intended resources explicitly.
Database, User and Grant resources are cluster-scoped, even when the Bootstrap itself is namespaced.
Their selector is therefore not limited by `watchNamespaces`.
The shipped RBAC covers `mysql.sql.crossplane.io`; selecting a different API group also requires corresponding permissions.

Readiness requires the expected objects and both Ready=True and Synced=True.
An empty selection or missing object cannot satisfy the wait.
Only pauses owned and recorded by that Bootstrap are resumed.
Database recreation requires `deletionPolicy: Orphan`, and finalizer removal requires explicit opt-in.
See [Bootstrap configuration](../configuration/bootstrap.md) for recreation, compensation and target safeguards.
