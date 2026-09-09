# Examples

Use these examples after completing the [installation target checks](installation.md#select-the-development-cluster).
Replace illustrative names, endpoints, storage classes and image choices with references from your development environment.

| Example | Start here when |
| --- | --- |
| [Minimal BackupPointer](examples/01-backuppointer-minimal.yaml) | You have a successful PXC backup and a bucket for its pointer. |
| [Policy](configuration/policy.md#admission-example) | You need column rules and optional SQL steps. |
| [Run from a pointer](configuration/run.md#admission-example) | You are ready to restore and transform one backup. |
| [Schedule](configuration/schedule.md#admission-example) | You want repeated Runs from a prepared template. |
| [Bootstrap](configuration/bootstrap.md#admission-example) | You want to restore output into existing development targets. |
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
   For the other resources, complete their configuration guide before applying the edited manifest.

> [!warning]
> Applying a Run starts work in a temporary database, and applying an unsuspended Schedule can create Runs.
> Applying a Bootstrap restores over its existing target databases.
