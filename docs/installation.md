# Installation

Install the published Helm chart into a development cluster with the Percona XtraDB Cluster Operator and its backup CRD already installed.
The manager reconciles all five API kinds, and the chart installs their schemas.
The manager requires the PXC backup API and fails startup when it is absent.

## Prerequisites

Use Helm 3, kubectl, and access to the intended development cluster.
Use a compatible PXC operator; the controller's PXC views target the `v1.20.0` contract.
Publishing pointers also needs an existing S3-compatible bucket and a same-namespace credential Secret, described in the [quickstart](quickstart.md).
The commands below pin the published chart `0.1.0-alpha.4`, whose manager and runner images default to `v0.1.0-alpha.4`.
The chart and images are public OCI artifacts in `ghcr.io/ydixken/pxc-anonymizer`.
The [prerequisites reference](reference/prerequisites.md) covers storage, credentials and optional Crossplane integration.

## Select the development cluster

These commands use Bash and leave the selected context explicit on every cluster operation.
Run them in the same Bash session from the repository root.

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

## Install with Helm

1. Inspect the published chart metadata before installing it.

   ```sh
   helm show chart oci://ghcr.io/ydixken/pxc-anonymizer/charts/pxc-anonymizer \
     --version 0.1.0-alpha.4
   ```

2. Install or upgrade the release in the confirmed development context.

   ```sh
   : "${DEV_CONTEXT:?Complete development-context selection first}"
   helm upgrade --install pxc-anonymizer \
     oci://ghcr.io/ydixken/pxc-anonymizer/charts/pxc-anonymizer \
     --version 0.1.0-alpha.4 \
     --namespace pxc-anonymizer-system --create-namespace \
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

## Install with Argo CD

Use this Application as an alternative to the Helm installation above, with Argo CD already installed in the selected cluster.
The example uses its `argocd` namespace and `default` project; adjust those to your installation and permit the repository and destination in that project.
Argo CD's [Helm-OCI source format](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/#declarative) separates the chart name from the registry path and omits `oci://` from `repoURL`.

1. Create the Application in your confirmed development cluster.

   ```sh
   : "${DEV_CONTEXT:?Complete development-context selection first}"
   kubectl --context "$DEV_CONTEXT" apply -f - <<'EOF'
   apiVersion: argoproj.io/v1alpha1
   kind: Application
   metadata:
     name: pxc-anonymizer
     namespace: argocd
   spec:
     project: default
     source:
       repoURL: ghcr.io/ydixken/pxc-anonymizer/charts
       chart: pxc-anonymizer
       targetRevision: 0.1.0-alpha.4
       helm:
         releaseName: pxc-anonymizer
     destination:
       server: https://kubernetes.default.svc
       namespace: pxc-anonymizer-system
     syncPolicy:
       syncOptions:
         - CreateNamespace=true
   EOF
   ```

2. Review the rendered resources in Argo CD and sync the Application.
   Then run the manager rollout and five-CRD checks from the Helm procedure above.

## Validate a manifest

Server-side dry-run checks a saved manifest against the installed API schemas without creating the resource.
It does not establish that referenced credentials, services, images or storage capacity are available.

1. Replace the example's illustrative values, save it as `example.yaml`, and validate it in the confirmed development context.
   Its namespace must already exist.

   ```sh
   : "${DEV_CONTEXT:?Complete development-context selection first}"
   kubectl --context "$DEV_CONTEXT" apply --dry-run=server -f example.yaml
   ```

> [!warning]
> Removing `--dry-run=server` allows controllers to act on the resource.
> Runs provision temporary databases, unsuspended Schedules create Runs, and Bootstraps restore existing targets.

## Build the demo command from source

The release `v0.1.0-alpha.4` predates `seed-demo`.
Follow the [demo-data guide](configuration/seed-demo.md) to build the command from this checkout and target a dedicated demo server.
The source command's availability does not imply that it exists in the pinned release image.

## Configuration boundaries

| Value | Purpose |
| --- | --- |
| `image.repository` | Manager image repository. |
| `image.tag` | Published image tag to deploy. |
| `image.pullPolicy` | Kubernetes image-pull policy. |
| `runner.image.repository` | Runner repository override; empty inherits the manager repository. |
| `runner.image.tag` | Runner tag override; empty inherits the manager tag. |
| `watchNamespaces` | Optional list limiting the manager's cache scope. |
| `leaderElection.enabled` | Leader election; defaults to `true`. |
| `crds.install` | Install the five CRDs; defaults to `true`. |
| `crds.keep` | Retain CRDs on uninstall; defaults to `true`. |

`watchNamespaces` limits what the cache watches; it does not reduce the chart's cluster-wide RBAC permissions.
The chart provides the manager ServiceAccount, RBAC, health probes and authenticated HTTPS metrics.
The chart supplies the resolved runner image through the non-secret `PXC_ANONYMIZER_RUNNER_IMAGE` environment variable.
The manager's `--runner-image` flag defaults to that value; an explicit flag overrides the environment, and a Run's `spec.runner.image` takes precedence over both.
Monitoring dashboards and alert rules are outside this installation.
CRDs are shared cluster resources, and their retention is deliberate because deleting a CRD deletes its custom resources.
For an upgrade, use the same release name and inspect the chart's CRD changes before applying them.
