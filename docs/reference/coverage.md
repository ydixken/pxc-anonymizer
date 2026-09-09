# V1 coverage

Use these mappings to translate a legacy configuration into an `AnonymizationPolicy` and an `AnonymizationRun`.
The operator does not import a legacy input file or execute a file path from one.
The [Policy guide](../configuration/policy.md) describes the admitted fields and reference rules.

## Configuration keys

Table (a) maps configuration structure without carrying database names, predicates or file contents from a deployment.

| V1 construct | V2 destination | Migration rule |
| --- | --- | --- |
| `databases.<database>` | `spec.databases[].name` | Make each database entry explicit. |
| Database-name matching | `spec.databases[].namePattern` | Translate and validate a whole-name RE2 pattern. |
| `databases.<database>[].table` | `tables[].name` | Attach the table to its database entry. |
| `columns.<column>` | `columns[].name`, `strategy`, `params` | Replace strategy expressions with structured fields. |
| `ignoreConditions` table/condition entries | `tables[].ignore.where` | Preserve matching rows with a WHERE expression. |
| `file.path` | `spec.steps[].configMapKeyRef` or `secretKeyRef` | Put reviewed SQL in a same-namespace object key. |
| `file.mode: pre` | `databases[].pre[].name` | Reference the named step before table transformations. |
| `file.mode: post` | `databases[].post[].name` | Reference the named step after table transformations. |
| Omitted `file.mode` | Explicit `pre` or `post` reference | Choose the intended phase during migration. |
| YAML anchors and aliases | Expanded YAML values | Reuse manifest fragments before admission. |

Expand shared ignore definitions onto the intended database and table entries.
`ignore.where` excludes matching rows from anonymization; it does not execute a complete SQL statement.
The expression must omit the `WHERE` keyword and statement delimiters and fit the 4096-character limit.
YAML aliases do not create a shared runtime object or a cross-resource reference.

For whole-table removal, use `action: Truncate` without column rules.
For a fixed replacement, use `strategy: constant` with `params.value`, or a same-namespace `params.valueFrom` Secret key for sensitive content.
Select those operations only when they express the intended transformation.
There is no filesystem-backed constant or inline SQL-statement field in a Policy.

## Strategy names

Table (b) follows the runner's explicit legacy-name mapping.
Use only the canonical V2 spelling in `columns[].strategy`; admission does not accept legacy aliases or function-call syntax.
The [strategy reference](strategies.md) covers every V2 strategy, parameter and runtime constraint.

| V1 spelling | V2 strategy | Migration note |
| --- | --- | --- |
| `first_name` | `firstName` | Review locale and column capacity. |
| `last_name` | `lastName` | Review locale and column capacity. |
| `name` | `fullName` | Review locale and column capacity. |
| `job` | `jobTitle` | Review locale and column capacity. |
| `ascii_email`, `email` | `email` | `params.ascii` defaults to true. |
| `phone_number` | `phone` | Set `params.digits` when overriding its default. |
| `date` | `date` | Set bounds and output format explicitly when needed. |
| `address` | `address` | Review the generated address format. |
| `street_address` | `streetAddress` | Review locale and column capacity. |
| `street_suffix_long` | `streetSuffix` | Review locale and column capacity. |
| `city` | `city` | Review locale and column capacity. |
| `postalcode` | `postalCode` | Review locale and column capacity. |
| `state` | `state` | Review locale and column capacity. |
| `country` | `country` | Review locale and column capacity. |
| `company` | `company` | Review generated values and column capacity. |
| `company_suffix` | `companySuffix` | Review generated values and column capacity. |
| `vat_id` | `vatId` | Runtime generation supports country `DE`. |
| `ipv4_public` | `ipv4Public` | Produces a public IPv4 address. |
| `url` | `url` | Review generated values and column capacity. |
| `uuid`, `uuid4` | `uuid` | Reserve 36 characters for the output. |
| `alphanumeric` | `alphanumeric` | Supply required `params.length`. |
| `alphanumeric_upper_case` | `alphanumeric` | Set `params.case: Upper` and `params.length`. |
| `random_number` | `digits` | Supply `params.length`; output is a digit string. |
| `null` | `null` | Quote `"null"` in YAML to keep a string. |
| `random_letters` | No supported mapping | Rejected at admission; choose a supported strategy explicitly. |

For legacy `alphanumeric(n)`, `alphanumeric_upper_case(n)` and `random_number(n)`, move the numeric argument to `params.length`.
The length must be between 1 and 4096.
Other strategy parameters use named V2 fields rather than positional arguments.
`random_letters` never had a supported implementation and is not an alias for `alphanumeric`.

Applicable locale-aware strategies support `en`; unsupported locales fail instead of falling back.
Custom Unicode email domains require `params.ascii: false`.
Review null/empty handling, consistency, data types, widths and unique constraints when migrating each column.
The default `Keep` behavior preserves null and empty inputs; use `onNull: Generate` or `onEmpty: Generate` when replacement is required.

## Environment and orchestration

Table (c) maps the legacy environment interface to resource fields or explicit exclusions.
V2 resources do not read these legacy variables.

| V1 interface | V2 destination | Migration rule |
| --- | --- | --- |
| `CREATE_PERCONA_CLUSTER` | Run `spec.tempCluster` | Temporary-cluster creation is mandatory; direct mode against an existing database is not exposed. |
| `PROCESS_PER_TABLE` | Operator-managed table traversal | Select tables in the Policy; there is no traversal-mode switch. |
| `LIMIT` | Run `spec.runner.pageSize` | Set the page size, not a cap on total processed rows. |
| `DRY_RUN` | Run `spec.runner.dryRun` | Validate transformations without changing table data. |
| `DB_HOST/USER/PASSWORD` | Run `spec.tempCluster.systemUsersSecretRef` | Supply source credentials through a Secret; the operator selects the temporary database connection. |
| `MINIO_*` | Object-storage fields and credential Secret references | Split endpoint, bucket and credentials; keep credential values in Secrets. |
| `WORKER_PROCESSES` | Run `spec.runner.workers` | Set the worker count within the admitted range. |
| `NAMESPACE` | `metadata.namespace` | Keep resource references in the consumer's namespace. |
| `PUBLIC_BUCKET` | PointerTarget `objectStorage.bucket` | Configure the pointer bucket separately from backup storage. |
| `PROD_BOOTSTRAP_INFO` | Run `spec.source.pointer` | Select HTTP or S3 to read the source pointer. |
| `BOOTSTRAP_SOURCE` | Bootstrap `spec.pointer` | Select HTTP, S3 or a literal backup destination. |
| Endpoint-selection ConfigMap toggle | Object-storage and restore `endpointURL` | Configure the endpoint directly; no provider toggle is required. |

PointerTarget is used by BackupPointer `spec.target` and Run `spec.output.pointer`.
For periodic execution, place the mapped Run spec under Schedule `spec.template.spec` and configure its schedule and concurrency policy in the [Schedule guide](../configuration/schedule.md).
For downstream restores, select existing clusters with Bootstrap `spec.targets` and explicitly configure any Crossplane coordination in the [Bootstrap guide](../configuration/bootstrap.md).

## Migration boundaries

> [!important]
> A structural mapping does not prove that referenced legacy SQL files are portable.
> Review their contents, execution order and transaction behavior before selecting SQL steps.
> Referenced SQL-file contents were outside the input-file coverage audit.

SQL bodies belong in ConfigMap or Secret keys referenced by named steps, with sensitive SQL kept in Secrets.
Preserve the intended order in each database's `pre` and `post` lists.
Arbitrary DDL can commit partially, and an uncertain step outcome blocks automatic replay.
See [SQL references](../configuration/policy.md#sql-references) and [lifecycle operations](../operations/lifecycle.md) before migrating file-based maintenance.

Operations that require host scripts, filesystem access or SQL execution outside these references have no Policy field.
Treat those as explicit migration gaps rather than silently dropping them or placing SQL in a strategy name.
These mappings cover configuration keys, strategy names and the legacy environment interface; they do not import or execute a legacy deployment.
