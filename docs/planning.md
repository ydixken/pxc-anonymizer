# Anonymization planning

Plan what data may leave the source before creating a Policy or enabling a Schedule.
We transform a restored temporary database, then publish a new backup; the Run does not modify the source cluster in place.
Use this checklist with the [strategy reference](reference/strategies.md) and the [pipeline guide](operations/pipeline.md).

## Inventory data and constraints

- Identify every schema, table and column that can contain personal or confidential data, including free text and auxiliary tables.
- Record a non-null paging key for each anonymized table: the primary key by default, or an explicit complete unique-index override in `primaryKey`; preserve those columns during transformation.
- Check that target tables use InnoDB so row changes and checkpoints can commit together.
- Match each strategy to column type, length, nullability and uniqueness constraints.
- Identify relationships that must remain valid across tables, and test representative joins against a sanitized fixture.
- Decide which records must survive unchanged, including service accounts, and express the intended ignore rules explicitly.

Admission and Policy Valid check rules and references, not the completeness of this data inventory or the source database schema.
Unselected data and deliberately ignored records still travel in a physical backup.
Review that residual data before considering an output suitable for wider access.

## Choose deterministic inputs and SQL steps

- Choose PerRun for a fresh execution seed or Fixed when separate Runs must share deterministic inputs.
- For Fixed mode, assign an owner for the same-namespace seed Secret, its access controls and a seed of at least 32 bytes; do not put the seed in the Policy or examples.
- Set an explicit date upper bound when date output must remain identical across Runs created on different days.
- Inventory database pre/post steps and store public SQL in ConfigMaps or sensitive SQL in Secrets.
- Review what each SQL step changes and how it behaves if execution stops; arbitrary MySQL DDL and a checkpoint cannot be assumed atomic.

The Policy hash covers its spec and references, not the referenced Secret payloads.
A Run separately freezes resolved SQL, seeds and sensitive values in its immutable Secret snapshot.
Use [Policy configuration](configuration/policy.md) and [the SQL-reference example](examples/05-policy-sql-steps.yaml) to prepare those references.

## Budget temporary resources and output retention

- Select PXC and backup images compatible with the source, an existing storage class and enough capacity for the restored database and processing overhead.
- Keep aggregate temporary PXC storage within the 20Gi limit; the volume size is per replica.
- Include overlapping Schedule Runs and retained failures in the capacity budget.
- Provide compatible source system-user passwords and backup credentials in the Run's namespace before execution.
- Choose output backup retention separately from Schedule Run-history limits and temporary-cluster retention.
- Decide who can read each backup and pointer location, including whether a public pointer exposes a sensitive destination name.

The controller's storage limit does not establish that a backup will fit.
Anonymization does not automatically make every retained table safe, and a retained temporary cluster can still contain source data.
See [storage sizing](operations/temp-cluster.md), [lifecycle and retention](operations/lifecycle.md) and [Schedule configuration](configuration/schedule.md).

## Prepare downstream restoration

- Identify the existing PXC target clusters and their namespace; inspect their identities before restoring over them.
- Choose the pointer transport and maximum publication age, and provision each target's restore credentials.
- Inventory the cluster-scoped Crossplane Database, User and Grant resources and select only the intended set.
- Record existing human pauses and review deletion policies before considering managed-resource recreation.
- Identify who owns application password Secrets and who will verify actual application-user login and permissions after restoration.

Bootstrap Complete covers target restores, readiness and configured Crossplane coordination.
It does not establish that the application's credentials or connection path work.
Follow [Bootstrapping a downstream cluster](operations/bootstrap.md) for the execution and recovery boundaries.

## Assign operating responsibility

Choose who reviews failed Runs, stale pointers, pending cleanup and downstream failures, and who owns the corresponding monitoring response.
Agree on Schedule cadence, missed-tick deadlines, concurrency and manual-run authorization.
Keep generated resources under their controller's ownership instead of adopting them into conflicting GitOps pruning.

Use [the examples](examples.md) to validate API shape, then use focused unit and admission checks for behavior covered by the project.
Schema acceptance does not demonstrate database compatibility, successful transformation or an application login.
Plan any environment-specific acceptance separately; these guides do not promise an expanded end-to-end harness or prescribe another live milestone run.
