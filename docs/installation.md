# Installation

Install the chart from this repository into a development cluster with the Percona XtraDB Cluster Operator and its backup CRD already installed.
The M1 manager reconciles BackupPointer only; the chart installs all five API schemas.
The manager requires the PXC backup API and fails startup when it is absent.

## Prerequisites

Use Helm 3, kubectl, and access to the intended development cluster.
Install a PXC operator compatible with the backup objects you use; this release's PXC views target the `v1.20.0` contract.
Publishing pointers also needs an existing S3-compatible bucket and a same-namespace credential Secret, described in the [quickstart](quickstart.md).
An image must be published and readable by your cluster before installation.
The image repository is `ghcr.io/ydixken/pxc-anonymizer`; choose an existing image tag from the [repository container packages](https://github.com/users/ydixken/packages?repo_name=pxc-anonymizer).
The commands below do not assume an unpublished tag is available.

## Select the development cluster

These commands use Bash and leave the selected context explicit on every cluster operation.
Run them from the repository root.

1. List the configured contexts, then enter the development context you intend to use.

   ```sh
   kubectl config get-contexts
   printf 'Development context: '
   read -r DEV_CONTEXT
   : "${DEV_CONTEXT:?A development context is required}"
   export DEV_CONTEXT
   kubectl --context "$DEV_CONTEXT" cluster-info
   ```

2. Inspect the cluster and required API before making changes.
   Confirm the displayed endpoint belongs to your development environment.

   ```sh
   kubectl --context "$DEV_CONTEXT" get namespaces
   kubectl --context "$DEV_CONTEXT" get crd perconaxtradbclusterbackups.pxc.percona.com
   printf 'Re-enter the confirmed development context: '
   read -r CONFIRMED_CONTEXT
   test "$CONFIRMED_CONTEXT" = "$DEV_CONTEXT"
   ```

## Install the chart

1. Select a published image tag and confirm the chart path exists in this checkout.

   ```sh
   printf 'Published image tag: '
   read -r IMAGE_TAG
   : "${IMAGE_TAG:?A published image tag is required}"
   export IMAGE_TAG
   test -f charts/pxc-anonymizer/Chart.yaml
   helm show chart ./charts/pxc-anonymizer
   ```

2. Install or upgrade the release in the confirmed development context.

   ```sh
   : "${DEV_CONTEXT:?Complete development-context selection first}"
   : "${IMAGE_TAG:?Select a published image tag first}"
   helm upgrade --install pxc-anonymizer ./charts/pxc-anonymizer \
     --namespace pxc-anonymizer-system --create-namespace \
     --set-string image.tag="$IMAGE_TAG" \
     --kube-context "$DEV_CONTEXT"
   ```

3. Verify the manager and all five CRDs.
   Do not treat an empty Deployment list as a successful installation.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n pxc-anonymizer-system \
     get deployments -l app.kubernetes.io/instance=pxc-anonymizer
   kubectl --context "$DEV_CONTEXT" -n pxc-anonymizer-system \
     rollout status deployment/pxc-anonymizer --timeout=180s
   kubectl --context "$DEV_CONTEXT" wait --for=condition=Established --timeout=60s \
     crd/backuppointers.pxc-anonymizer.io \
     crd/anonymizationpolicies.pxc-anonymizer.io \
     crd/anonymizationruns.pxc-anonymizer.io \
     crd/anonymizationschedules.pxc-anonymizer.io \
     crd/bootstraps.pxc-anonymizer.io
   ```

Continue with the [BackupPointer quickstart](quickstart.md).

## Configuration boundaries

| Value | Purpose |
| --- | --- |
| `image.repository` | Manager image repository. |
| `image.tag` | Published image tag to deploy. |
| `image.pullPolicy` | Kubernetes image-pull policy. |
| `watchNamespaces` | Optional list limiting the manager's cache scope. |
| `leaderElection.enabled` | Leader election; defaults to `true`. |
| `crds.install` | Install the five CRDs; defaults to `true`. |
| `crds.keep` | Retain CRDs on uninstall; defaults to `true`. |

`watchNamespaces` limits what the cache watches; it does not reduce the chart's cluster-wide RBAC permissions.
The chart provides the manager ServiceAccount, RBAC, health probes and authenticated HTTPS metrics.
Monitoring dashboards, alert rules and the later runner are outside this installation.
CRDs are shared cluster resources, and their retention is deliberate because deleting a CRD deletes its custom resources.
For an upgrade, use the same release name and inspect the chart's CRD changes before applying them.
