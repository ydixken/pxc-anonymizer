# pxc-anonymizer

pxc-anonymizer prepares anonymized Percona XtraDB Cluster backups for development and testing.
It restores a backup into a temporary cluster, applies a transformation policy and publishes the result for downstream restores.

## The workflow

| Resource | Responsibility |
| --- | --- |
| [BackupPointer](configuration/backuppointer.md) | Select a successful PXC backup and publish its location as JSON. |
| [AnonymizationPolicy](configuration/policy.md) | Describe transformations and validate their references. |
| [AnonymizationRun](configuration/run.md) | Restore, transform, back up, publish and clean up one execution. |
| [AnonymizationSchedule](configuration/schedule.md) | Create Runs on a cron schedule or manual request. |
| [Bootstrap](configuration/bootstrap.md) | Restore published output into existing development targets. |

A BackupPointer can publish an existing backup independently of the anonymization workflow.
Policies describe transformations; Runs execute them in temporary databases.
Schedules repeat Runs, while Bootstrap handles downstream restores and optional Crossplane coordination.

## Start here

1. Check the [prerequisites](reference/prerequisites.md) and [install the operator](installation.md) in your selected development cluster.
2. Follow the [BackupPointer quickstart](quickstart.md) to publish and read a pointer.
3. Choose the next workflow from the [examples index](examples.md).

The [API reference](reference/api.md) describes all five schemas.
Use [conditions and reasons](reference/conditions.md) to interpret progress and the [pointer contract](operations/pointer.md) when consuming published JSON.

> [!warning]
> A pointer can identify an original, unanonymized backup.
> Confirm the data's origin and access policy before making a pointer or backup publicly readable.
