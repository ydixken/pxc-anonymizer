# Examples

Use these examples after completing the [installation target checks](installation.md#select-the-development-cluster).
Replace illustrative names, endpoints, storage classes and image choices with references from your development environment.

| Example | Start here when |
| --- | --- |
| [Minimal BackupPointer](examples/01-backuppointer-minimal.yaml) | You have a successful PXC backup and a bucket for its pointer. |
| [Minimal email Policy](examples/02-policy-minimal.yaml) | You need an email rule for an existing table. |
| [Run from a BackupPointer](examples/03-run-from-pointer.yaml) | A BackupPointer and Policy are ready. |
| [Run from an explicit Percona backup](examples/04-run-explicit-backup.yaml) | You want to select a successful backup directly. |
| [Policy with SQL references](examples/05-policy-sql-steps.yaml) | You need SQL from ConfigMaps or Secrets. |
| [Weekly Schedule](examples/06-schedule-weekly.yaml) | You want repeated Runs from a prepared template. |
| [Minimal Bootstrap](examples/07-bootstrap-minimal.yaml) | You want to restore output into an existing development target. |
| [Bootstrap with Crossplane](examples/08-bootstrap-crossplane.yaml) | You need to pause database reconcilers around a restore. |
| [API field reference](examples/09-reference.yaml) | You need optional fields for all five resource kinds. |
| [Demo dataset](configuration/seed-demo.md) | You need deterministic source data on a dedicated demo server. |

## Validate before applying

Server-side dry-run checks the installed schema without creating the resource.
It does not resolve referenced Secrets, validate image compatibility or reserve storage capacity.

1. Save and edit the example you need, then validate it with the development context you confirmed during installation.

   ```sh
   : "${DEV_CONTEXT:?Complete the installation target checks first}"
   kubectl --context "$DEV_CONTEXT" apply --dry-run=server \
     -f docs/examples/01-backuppointer-minimal.yaml
   ```

2. Follow the [BackupPointer quickstart](quickstart.md) to prepare its references and publish a pointer.
   Complete the [Policy](configuration/policy.md), [Run](configuration/run.md), [Schedule](configuration/schedule.md) or [Bootstrap](configuration/bootstrap.md) guide before applying those examples.

> [!warning]
> Applying a Run starts work in a temporary database, and applying an unsuspended Schedule can create Runs.
> Applying a Bootstrap restores over its existing target databases.
