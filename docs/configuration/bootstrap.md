# Bootstrap

`Bootstrap` defines a pointer or backup destination and an ordered set of existing PXC clusters to restore.
M1 installs and validates this API but does not implement the Bootstrap controller.
Creating a Bootstrap does not restore a database, pause Crossplane or recreate managed resources.

## Admission example

The example is an API contract for validation, not a runnable restore in M1.

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
Optional `pointer.maxAge` defines the allowed pointer publication age; omission disables the age check under the restore contract.

`spec.restore` is required and uses [RestoreS3Credentials](../reference/api.md#restore-credentials-and-container-arguments).
`spec.targets` is required and must contain at least one entry.
Targets are a map list keyed by `pxcCluster`, so duplicate cluster names are rejected.
Each target requires a nonempty `pxcCluster` and can override `restore` and `containerOptions`.
The execution contract restores targets in their declared order and in the Bootstrap's namespace.

`spec.trigger` is an optional token intended to re-arm a terminal Bootstrap when changed.
M1 does not act on that token.

## Crossplane configuration

Omit `spec.crossplane` when no Crossplane coordination is wanted.
The fields below describe the later controller contract and do not authorize M1 to change managed resources.

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
The planned runtime guard permits Database recreation only with an Orphan deletion policy; admission does not inspect that policy.
Do not treat schema acceptance as a safety check for destructive restore or recreation behavior.

## Timeouts

| Field under `timeouts` | Default |
| --- | --- |
| `clustersReady` | `0s`, meaning unbounded under the execution contract. |
| `pointer` | `15m`. |
| `restore` | `3h` for each target. |
| `crossplane` | `15m`. |

The schema stores these settings; M1 does not start the corresponding timers.

## Status contract

Status contains `observedGeneration`, `observedTrigger`, `conditions`, `source`, `targets`, `targetsSummary`, `crossplane`, `history`, `startedAt` and `completedAt`.
`source` uses [ResolvedSource](../reference/api.md#shared-status-shapes).
The phase enum is `Pending`, `WaitingForClusters`, `ResolvingPointer`, `PausingCrossplane`, `Restoring`, `ResumingCrossplane`, `RecreatingCrossplane`, `Completed` or `Failed`.

Each target status has `pxcCluster`, `restoreName`, `state`, `startedAt`, `completedAt` and `message`.
`targetsSummary` carries the completed/total printer value.
Crossplane status lists `paused`, `resumed`, `recreated` and `pending` objects, each identified by `kind` and `name`.
`history` holds at most five records containing `trigger`, `startedAt`, `completedAt`, `outcome` and `destination`.

The condition contract includes `ClustersReady`, `PointerResolved`, `CrossplanePaused`, `Restored`, `CrossplaneResumed`, `CrossplaneRecreated`, `Complete` and `Failed`.
M1 does not populate Bootstrap status.
The short name is `bs`, with Phase, Destination, Targets, Complete and Age columns.
