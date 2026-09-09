# Bootstrap

`Bootstrap` defines a pointer or backup destination and an ordered set of existing PXC clusters to restore.
Its controller restores those targets sequentially, with optional Crossplane pause, recreation and readiness checks.

## Admission example

Replace the illustrative pointer, target and credential references before execution.

> [!warning]
> A Bootstrap restores over existing target databases.
> Confirm the development targets and source backup before applying it without dry-run.

```yaml
apiVersion: pxc-anonymizer.io/v1alpha1
kind: Bootstrap
metadata:
  name: example-bootstrap
  namespace: database-demo
spec:
  pointer:
    http:
      url: https://objects.example.com/example-pointers/latest.json
    maxAge: 48h
  restore:
    credentialsSecret: backup-credentials
    endpointURL: https://objects.example.com
  targets:
    - pxcCluster: example-target
```

Use the [admission-only procedure](../reference/api.md#admission-only-examples) to validate it.

## Pointer, restore and targets

`spec.pointer` is required and immutable after creation.
It requires exactly one of `http`, `s3` or `destination`.
The HTTP and S3 forms use the [shared pointer-source fields](../reference/api.md#pointer-targets-and-sources).
A literal destination must match `^s3://.+$`.
The runtime rejects a literal destination combined with `maxAge`, because it has no pointer publication timestamp to verify.
Optional `pointer.maxAge` defines the allowed pointer publication age; omission disables the age check under the restore contract.
When checking a JSON pointer's age, stale or future timestamps and a missing v2 publication timestamp fail validation.
A legacy v1 pointer without a publication timestamp skips the age check with a `LegacyPointerAgeUnknown` Event.

`spec.restore` is required and uses [RestoreS3Credentials](../reference/api.md#restore-credentials-and-container-arguments).
`spec.targets` is required and must contain at least one entry.
Targets are a map list keyed by `pxcCluster`, so duplicate cluster names are rejected.
Each target requires a nonempty `pxcCluster` and can override `restore` and `containerOptions`.
Target cluster names must fit Percona's 22-byte limit.
Generated restore names are deterministically shortened when necessary to keep Percona's derived Job labels within 63 bytes, while retaining each execution's distinct identity.
The execution contract restores targets in their declared order and in the Bootstrap's namespace.
An omitted restore endpoint falls back to the S3 endpoint recorded in the pointer.
Admission defaults the region to `auto`; only an actually empty region inherits the pointer's region.
The resolved non-secret references are frozen for each target.

`spec.trigger` is an optional token that re-arms a terminal Bootstrap when changed.

## Crossplane configuration

Omit `spec.crossplane` when no Crossplane coordination is wanted.
These managed resources are cluster-scoped; they are not limited to the Bootstrap namespace.
Use `selector` to identify the intended target set, because omission selects all matching resources of the configured kinds.

| Field under `crossplane` | Contract |
| --- | --- |
| `group` | Defaults to `mysql.sql.crossplane.io`. |
| `version` | Defaults to `v1alpha1`. |
| `kinds` | Set of plural resources; defaults to `databases`, `users`, `grants`. |
| `selector` | Optional label selector; omission selects all matching resources in scope. |
| `pause` | Defaults to `true`. |
| `resume` | Defaults to `true`. |
| `recreate` | Optional recreation settings; omission disables recreation. |
| `readinessTimeout` | Defaults to `15m`. |

`recreate.kinds` defaults to `users` and `grants`.
`recreate.removeFinalizers` defaults to `false`, and `recreate.waitForRecreation` defaults to `true`.
The runtime guard permits Database recreation only with an Orphan deletion policy; admission does not inspect that policy.
Only resources this Bootstrap paused are recorded for resume, with UID and pause-owner checks preserving preexisting pauses.
Readiness requires the expected resources and both `Ready=True` and `Synced=True`; an empty match is not success.
Recreation persists the original UID before deletion, then waits for disappearance and a ready replacement instead of treating the old object as recreated.
Do not treat schema acceptance as a safety check for destructive restore or recreation behavior.

## Timeouts

| Field under `timeouts` | Default |
| --- | --- |
| `clustersReady` | `0s`, meaning unbounded under the execution contract. |
| `pointer` | `15m`. |
| `restore` | `3h` for each target. |
| `crossplane` | `15m`. |


## Restore controller contract

The controller records its cleanup finalizer and execution inputs before external changes, then waits for target readiness and restores targets in their declared order.
Each execution has a persisted unique identity, so changing a trigger back to an older value starts a distinct restore execution.
Restore creation intent is recorded before the API call; recovery adopts a matching owned child, while an absent child after recorded intent fails and enters compensation.
That safe failure can follow a crash before Create was issued; inspect the reported condition before rearming.
It accepts only the actual Succeeded restore state; Starting Cluster remains active.
After each restore it waits for cluster readiness before proceeding to the next target or resuming Crossplane.

Failure and deletion wait for a nonterminal restore instead of deleting it.
If an incomplete restore disappears, compensation waits until its matching restore Job is absent before unpausing the original target UID, even when the restore UID was never recorded.
An invalid execution checkpoint fails before new effects while retaining the frozen references needed for compensation.
Only recorded pauses owned by this Bootstrap are resumed, preserving a human pause on an already completed target.
Completed is absorbing until the trigger changes; rearming completes compensation, archives at most five outcomes and resets execution.

## Status contract

Status contains `observedGeneration`, `observedTrigger`, `conditions`, `execution`, `source`, `targets`, `targetsSummary`, `crossplane`, `history`, `startedAt` and `completedAt`.
`source` uses [ResolvedSource](../reference/api.md#shared-status-shapes).
The phase enum is `Pending`, `WaitingForClusters`, `ResolvingPointer`, `PausingCrossplane`, `Restoring`, `ResumingCrossplane`, `RecreatingCrossplane`, `Completed` or `Failed`.

Each target status has `pxcCluster`, `restoreName`, `state`, `startedAt`, `completedAt` and `message`.
`targetsSummary` carries the completed/total printer value.
Crossplane status lists `paused`, `resumed`, `recreated` and `pending` objects, identified by `kind`, `name` and optional `uid`.
Its `recreating` records persist `kind`, `name`, the original `uid`, `deletionObserved` and `complete` across reconciliations.
`execution` stores `id`, `specHash` excluding the trigger, frozen `crossplaneGroup`, `crossplaneVersion`, selected resource identities and target execution records.
Each execution target stores `pxcCluster`, its original `uid`, `restoreUID`, `restoreAttempted` and resolved `restore` references; no Secret payloads are stored in status.
`history` holds at most five records containing `trigger`, `startedAt`, `completedAt`, `outcome` and `destination`.

The condition contract includes `ClustersReady`, `PointerResolved`, `CrossplanePaused`, `Restored`, `CrossplaneResumed`, `CrossplaneRecreated`, `Complete` and `Failed`.
The short name is `bs`, with Phase, Destination, Targets, Complete and Age columns.
