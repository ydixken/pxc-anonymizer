# The pipeline end to end

The five resources separate source selection, transformation rules, execution, cadence and downstream restoration.
Prepare the inputs with [Anonymization planning](../planning.md) before enabling a Schedule or creating a Bootstrap.

## Follow the data and ownership

1. A BackupPointer selects a qualifying completed Percona backup and publishes a pointer document.
   Ready describes selection and publication; Fresh separately describes backup age.
2. A Policy validates transformation rules and references.
   Valid does not establish connectivity or compatibility with every source database schema.
3. A Schedule creates Runs from its template, or an operator creates a standalone Run.
   A Run resolves its source and freezes the Policy, credentials and SQL needed for that execution.
4. The Run provisions its own temporary PXC cluster, restores the source and executes anonymization there.
   It then creates an output backup, optionally publishes an output pointer and applies retention before temporary cleanup.
5. A separately created Bootstrap resolves a pointer or literal destination and restores existing downstream targets in order.
   Optional Crossplane coordination belongs to this execution, not to the Schedule.

A BackupPointer does not create a Run, and publishing a Run's output does not automatically create or rearm a Bootstrap.
Choose when downstream restoration occurs; changing a completed Bootstrap's trigger starts another execution after prior compensation finishes.
Publishing a newer pointer alone leaves that completed execution unchanged.

## Keep namespace and storage boundaries explicit

Policies, Runs and their referenced Kubernetes objects use the same namespace.
A same-namespace BackupPointer reference is a convenience; an HTTP or S3 pointer can carry the source location between otherwise isolated namespaces.
It grants no cross-namespace Kubernetes access and contains no credential payload.
Provision the destination namespace's credentials independently.

The Run needs source-compatible system-user passwords because a physical restore carries `mysql.user`.
Its resolved sensitive inputs live in an immutable Secret snapshot; non-secret Policy structure lives in the ConfigMap snapshot.
The temporary cluster, Restore and runner Job retain their recorded owner and UID boundaries.
Output backups deliberately outlive the Run, and a stored pointer object is not removed by Kubernetes owner garbage collection.
See [temporary cluster sizing and storage](temp-cluster.md) and [the pointer contract](pointer.md).

## Observe each boundary

Use selected status fields instead of dumping credential Secrets.
The commands below inspect existing resources in the development context confirmed during installation.

1. Select the namespace and inspect source and Policy conditions.

   ```sh
   : "${DEV_CONTEXT:?Set DEV_CONTEXT to the development context you confirmed}"
   PIPELINE_NAMESPACE=database-demo
   kubectl --context "$DEV_CONTEXT" -n "$PIPELINE_NAMESPACE" \
     get backuppointer latest-backup -o json |
     jq '{generation: .metadata.generation, conditions: .status.conditions,
       current: .status.current}'
   kubectl --context "$DEV_CONTEXT" -n "$PIPELINE_NAMESPACE" \
     get anonymizationpolicy example-policy -o json |
     jq '{generation: .metadata.generation, conditions: .status.conditions,
       hash: .status.hash}'
   ```

2. Select a created Run and inspect its execution and cleanup result.

   ```sh
   RUN_NAME=example-run
   kubectl --context "$DEV_CONTEXT" -n "$PIPELINE_NAMESPACE" \
     get anonymizationrun "$RUN_NAME" -o json |
     jq '{generation: .metadata.generation, phase: .status.phase,
       conditions: .status.conditions, source: .status.source,
       output: .status.output, tempCluster: .status.tempCluster}'
   ```

3. Inspect a separately created Bootstrap after the output is ready for downstream use.

   ```sh
   kubectl --context "$DEV_CONTEXT" -n "$PIPELINE_NAMESPACE" \
     get bootstrap example-bootstrap -o json |
     jq '{generation: .metadata.generation, phase: .status.phase,
       requestedTrigger: .spec.trigger, observedTrigger: .status.observedTrigger,
       conditions: .status.conditions, targets: .status.targets,
       crossplane: .status.crossplane}'
   ```

Apply the [generation checks](../reference/conditions.md#generations-and-waits) to each relevant condition.
An earlier successful stage can remain True after a later failure, and Schedule Ready is not its latest Run's result.
Run Complete includes successful temporary cleanup; a failed Run can have a completion timestamp while cleanup remains active.
Bootstrap Complete covers restores and configured coordination, not an application-user login check.

## Operate failures through their owner

Keep generated children out of conflicting GitOps ownership or pruning.
An unexpectedly missing recorded Restore or runner Job fails closed instead of being recreated as an assumed retry.
Use [Run lifecycle handling](lifecycle.md), [Bootstrap recovery](bootstrap.md) and [Argo CD guidance](argocd.md) for the corresponding owner.
Local manifest admission checks validate API shape; they do not prove a restore, transformation or downstream application session.
