# Bootstrapping a downstream cluster

Bootstrap restores an existing PXC cluster; it does not create a downstream cluster or anonymize its source.
Use an output whose transformations have already been reviewed.
The [configuration reference](../configuration/bootstrap.md) defines source formats, timeouts and optional Crossplane settings.

> [!warning]
> Restoring replaces data in the selected targets.
> Confirm the development context, target identities and source before creating or rearming a Bootstrap.

## Prepare the targets

Prepare existing same-namespace PXC targets, compatible backup tooling and S3 credentials using the standard AWS key names.
Choose a bounded initial `clustersReady` timeout when an unlimited readiness wait is unsuitable.
The default restore timeout is three hours per target, so budget a multi-target execution accordingly.

Use [example 07](../examples/07-bootstrap-minimal.yaml) without Crossplane coordination or [example 08](../examples/08-bootstrap-crossplane.yaml) with an explicit selector.
Adapt and validate one manifest before execution.
Neither manifest creates PXC clusters, credential Secrets or Crossplane managed resources.

1. Select the confirmed development context and inspect the target in the adapted minimal example.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   BOOTSTRAP_NAMESPACE=database-demo
   BOOTSTRAP_NAME=example-bootstrap
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     get perconaxtradbcluster example-downstream -o json |
     jq '{name: .metadata.name, uid: .metadata.uid,
       deleting: .metadata.deletionTimestamp, paused: .spec.pause,
       state: .status.state, pxc: .status.pxc, haproxy: .status.haproxy}'
   ```

2. For the Crossplane example, inspect the cluster-scoped selected set before enabling coordination.

   ```sh
   kubectl --context "$DEV_CONTEXT" \
     get databases.mysql.sql.crossplane.io,users.mysql.sql.crossplane.io,grants.mysql.sql.crossplane.io \
     -l app.kubernetes.io/part-of=database-demo -o json |
     jq '{count: (.items | length), resources: [.items[] |
       {kind, name: .metadata.name, uid: .metadata.uid,
        deletionPolicy: .spec.deletionPolicy,
        paused: .metadata.annotations["crossplane.io/paused"],
        conditions: .status.conditions}]}'
   ```

3. Validate the adapted minimal manifest against the installed API.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     apply --dry-run=server -f docs/examples/07-bootstrap-minimal.yaml
   ```

4. After confirming the target and source, create the adapted Bootstrap once.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     create -f docs/examples/07-bootstrap-minimal.yaml
   ```

A dry run does not prove that a target, credential or selected managed resource exists or works.
Treat an empty Crossplane selection as a configuration error, not evidence of readiness.
Review the real object count and identities before choosing the selector.

## Observe one execution

The controller records its finalizer, execution identity and target UIDs before effects.
It waits for targets, resolves one source, optionally pauses Crossplane, then restores targets in declared order.
Only Percona Succeeded ends a restore successfully; Starting Cluster remains active.
Each restored cluster must become ready before the next target or Crossplane stage proceeds.

1. Inspect the selected Bootstrap's execution, targets and coordination state.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     get bootstrap "$BOOTSTRAP_NAME" -o json |
     jq '{generation: .metadata.generation, phase: .status.phase,
       requestedTrigger: .spec.trigger, observedTrigger: .status.observedTrigger,
       executionID: .status.execution.id, conditions: .status.conditions,
       targets: .status.targets, crossplane: .status.crossplane}'
   ```

2. Wait for success within an observation window chosen for this execution.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     wait bootstrap/"$BOOTSTRAP_NAME" --for=condition=Complete --timeout=30m
   ```

3. Check that success describes the requested generation and trigger.

   ```sh
   set -o pipefail
   kubectl --context "$DEV_CONTEXT" -n "$BOOTSTRAP_NAMESPACE" \
     get bootstrap "$BOOTSTRAP_NAME" -o json |
     jq -e '.metadata.generation as $generation |
       ((.status.observedTrigger // "") == (.spec.trigger // "")) and
       any(.status.conditions[]?;
         .type == "Complete" and .status == "True" and
         .observedGeneration == $generation)'
   ```

The CLI observation window does not alter the controller's per-stage deadlines.
Inspect Failed and its reason when the wait expires; an expired local wait alone does not establish failure.
Complete confirms the configured restore and Crossplane stages.
It does not test an application-user login, password delivery, schema permissions or the application's connection path.
Validate those separately through the application's normal access before admitting application traffic.

## Crossplane ownership and recreation

Omitting `crossplane` leaves managed resources untouched.
When enabled, an omitted selector includes all resources of the configured cluster-scoped kinds.
Use the smallest intended set and preserve any pause that was already set by a human.

Bootstrap records only pauses it owns and resumes only those recorded identities with matching ownership.
The readiness check requires the selected resources to exist with both Ready and Synced True.
Keep the selected identities and group/version stable while an execution is active.

Recreation is optional and defaults to users and grants when enabled.
It checkpoints original UIDs before deletion and distinguishes replacement objects from the originals.
The example leaves `removeFinalizers` false; use normal provider finalizers.
Database recreation additionally requires an Orphan deletion policy at runtime.
Changing finalizer-removal settings is not a routine remedy for a timeout.

## Failure and deletion

Failed does not stop compensation.
The controller waits for a nonterminal owned Restore instead of deleting it and resumes only its recorded Crossplane pauses.
If a recorded Restore disappears, it fails closed; target-unpause compensation waits until the matching restore Job is absent and checks the original target UID.
This also covers an ambiguous create response where no Restore UID was saved.

Inspect the status target's `restoreName` and the corresponding Percona Restore before intervening.
Do not recreate that child, delete an active Restore, remove the Bootstrap finalizer or manually claim a human-owned pause.
Normal Bootstrap deletion waits for its compensation; the existing target cluster remains a separately managed resource.
See [conditions and reasons](../reference/conditions.md#bootstrap) and [Argo CD guidance](argocd.md).

## Start a later execution

A completed Bootstrap remains completed until its trigger changes.
After reviewing the previous outcome and allowing compensation to finish, change `spec.trigger` to a new token in the adapted manifest and reconcile it through its owner.
The pointer specification itself is immutable; use a new Bootstrap resource to select a different pointer configuration.
The controller archives at most five prior outcomes and allocates a distinct execution ID, even when a later trigger reuses an older token value.
