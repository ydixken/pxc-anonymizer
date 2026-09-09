# AnonymizationPolicy

`AnonymizationPolicy` describes database and column transformations without embedding SQL bodies or credentials.
Its controller validates rules and references and records a canonical policy hash.
A Run snapshots the validated policy and executes transformations in its temporary database.

## Admission example

This manifest describes a policy contract; creating it does not change a database.

```yaml
apiVersion: pxc-anonymizer.io/v1alpha1
kind: AnonymizationPolicy
metadata:
  name: example-policy
  namespace: database-demo
spec:
  determinism:
    mode: PerRun
  databases:
    - name: application
      tables:
        - name: customers
          columns:
            - name: email
              strategy: email
              params:
                domain: example.com
            - name: telephone
              strategy: "null"
```

Use the [admission-only procedure](../installation.md#validate-a-manifest) to validate it.

## Policy fields

`spec.databases` is required and must contain at least one entry.
It is an atomic list, so a pattern-only entry does not need an artificial `name` map key.

| Field | Contract |
| --- | --- |
| `determinism.mode` | `PerRun` by default, or `Fixed`. |
| `determinism.seedSecretRef` | Same-namespace Secret key; required for `Fixed`. |
| `defaults.pageSize` | Defaults to `5000`; maximum `50000`. |
| `defaults.onNull` | `Keep` by default, or `Generate`. |
| `defaults.onEmpty` | `Keep` by default, or `Generate`. |
| `defaults.locale` | Defaults to `en`. |
| `defaults.emailDomain` | Optional default domain for generated email values. |
| `steps` | Optional named SQL references, described below. |
| `databases` | Required database policies. |

The determinism contract uses a separate 32-byte seed per Run in `PerRun` mode and a referenced seed of at least 32 bytes for stable mappings in `Fixed` mode.
Seed values are raw Secret bytes, with no additional base64 decoding.

## SQL references

Each `steps` entry requires a `name` matching `^[a-z0-9-]{1,63}$` and exactly one of `configMapKeyRef` or `secretKeyRef`.
Both selectors identify a same-namespace object and key.
`continueOnError` defaults to false.
Database `pre` and `post` lists reference those step names through `{name: ...}` entries, preserving list order.
Use a Secret for SQL containing sensitive values.
Admission rejects missing or conflicting reference forms but does not read the referenced SQL or resolve step names.
SQL steps execute through the server's multi-statement support; do not split SQL files on semicolons.
DDL may commit partially even when a later statement fails, so SQL-step checkpoints do not promise transactional rollback of arbitrary SQL.
An incomplete durable step marker blocks automatic replay with `ErrUncertainOutcome`; inspect and explicitly resolve the uncertain step before resuming.

## Database and table rules

Each database entry requires exactly one of `name` or `namePattern`.
An exact name matches `^[A-Za-z0-9_$]{1,64}$`.
`namePattern` carries a RE2 pattern matched against the whole database name; explicit anchors are optional.
`optional` permits a missing database or zero pattern matches under the execution contract.
A pattern does not require `optional: true` at admission.
The remaining database fields are ordered `pre` and `post` references and a `tables` list keyed by table name.

| Table field | Contract |
| --- | --- |
| `name` | Required table name. |
| `action` | `Anonymize` by default, or `Truncate`. |
| `optional` | Optional missing-table policy. |
| `primaryKey` | Optional column list overriding the discovered primary key. |
| `ignore.where` | Required when `ignore` is present; at most 4096 characters, no semicolon. |
| `columns` | Column rules keyed by name. |
| `pageSize` | Optional table override; maximum `50000`. |

`Anonymize` requires at least one column.
Its target tables must use InnoDB so row changes and page checkpoints can commit or roll back together; nontransactional engines fail runtime preflight before writes.
`Truncate` forbids the `columns` field.
The `ignore.where` contract is a WHERE expression without the keyword, not a complete SQL statement.

## Columns and strategies

Each column rule requires `name` and `strategy`.
Optional `params` carries strategy parameters; `consistent`, `onNull` and `onEmpty` override their inherited behavior.
`onNull` and `onEmpty` accept `Keep` or `Generate`.
`consistent: true` requests equal output for equal input across tables within the Run.

The strategy enum is:

```text
firstName lastName fullName jobTitle email phone date address
streetAddress streetSuffix city postalCode state country company
companySuffix vatId iban ipv4Public url uuid alphanumeric digits
number text constant hash mask null
```

| Parameter | Schema contract |
| --- | --- |
| `locale`, `domain` | Optional locale and email-domain overrides; applicable faker strategies support locale `en`. |
| `ascii` | Optional boolean; defaults to `true`. |
| `digits` | Defaults to `15`; maximum `15`. |
| `from`, `to` | Date bounds; `from` defaults to `1950-01-01`. |
| `format` | Optional `date` or `datetime`. |
| `country` | Defaults to `DE`; VAT and IBAN execution support `DE`. |
| `length` | Optional integer; maximum `4096` through the column rule. |
| `case` | Optional `Mixed`, `Upper` or `Lower`. |
| `unique` | Optional boolean requesting collision retries. |
| `min`, `max` | Optional integer bounds. |
| `maxLength`, `sentences` | Optional text-generation limits. |
| `value` | Optional string constant; an empty string is preserved. |
| `valueFrom` | Optional same-namespace Secret key for a constant. |
| `encoding` | Optional `hex` or `base64`. |
| `keepPrefix`, `keepSuffix`, `maskChar` | Optional masking settings. |

`alphanumeric` and `digits` require `params.length`.
`constant` requires `params.value` or `params.valueFrom`; the schema does not require those two fields to be mutually exclusive.
`null`, `mask` and `constant` forbid the `consistent` field, including an explicit false value.
The schema rejects unknown strategies and enum values; it does not enforce every strategy-specific runtime constraint.
Custom Unicode email domains require `params.ascii: false`; omitted or true rejects them instead of silently producing non-ASCII output.
Locale validation applies to name, job, address and geography strategies; unsupported values fail explicitly instead of falling back to English.
VAT and IBAN reject unsupported countries instead of generating a different country's format.
These runtime limits do not add enums to the permissive locale and country schema fields.
An omitted date upper bound uses the date of the Run's creation timestamp, which stays fixed across retries.
For identical Fixed-mode date outputs across Runs created on different days, set an explicit date upper bound.
Hash output defaults to 64 characters for hex and 44 for standard base64; an explicit length must be at least 1 and no greater than the encoding's default length.
Hashes are truncated when requested, never extended through padding or repetition.
UNIQUE collisions use at most eight deterministic row-specific attempts; successful rows are not regenerated during a collision retry.

## Status contract

`status.hash` contains `sha256:` followed by 64 lowercase hexadecimal characters from canonical spec-only JSON.
Resolved SQL, constant values from Secrets and seed bytes are excluded from this hash; their references remain part of the spec.
The `Valid` condition uses reasons `SpecValid`, `StepRefMissing` or `PatternInvalid`.
The short name is `apol`.

## Validation controller contract

The controller validates rules and resolves SQL ConfigMap/Secret keys, a fixed seed reference and constant-value Secret references in the Policy namespace.
It creates no children and changes no database contents.
`Valid=True/SpecValid` and the hash are written only after every required check succeeds.
A missing reference sets `Valid=False/StepRefMissing`, an invalid pattern or rule sets `PatternInvalid`, and invalidation clears a previously computed hash.
Conditions identify the generation they describe.
Referenced ConfigMap changes enqueue the matching Policies; Secret changes are picked up by a ten-minute refresh without listing or watching Secrets.
Runs use a separate immutable snapshot, keeping sensitive resolved content in Secrets rather than the policy ConfigMap or logs.
