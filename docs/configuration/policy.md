# AnonymizationPolicy

`AnonymizationPolicy` describes database and column transformations without embedding SQL bodies or credentials.
M1 installs this API and enforces its admission rules, but does not run the Policy controller or transformation engine.
Reference resolution, pattern compilation, policy hashing and transformation execution are deferred controller behavior.

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

Use the [admission-only procedure](../reference/api.md#admission-only-examples) to validate it.

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

The determinism contract uses a separate seed per Run in `PerRun` mode and a referenced seed for stable mappings in `Fixed` mode.
M1 does not create or use these seeds.

## SQL references

Each `steps` entry requires a `name` matching `^[a-z0-9-]{1,63}$` and exactly one of `configMapKeyRef` or `secretKeyRef`.
Both selectors identify a same-namespace object and key.
`continueOnError` defaults to false.
Database `pre` and `post` lists reference those step names through `{name: ...}` entries, preserving list order.
Use a Secret for SQL containing sensitive values.
Admission rejects missing or conflicting reference forms but does not read the referenced SQL or resolve step names.

## Database and table rules

Each database entry requires exactly one of `name` or `namePattern`.
An exact name matches `^[A-Za-z0-9_$]{1,64}$`.
`namePattern` carries the intended anchored RE2 pattern; compilation awaits the Policy controller.
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
`Truncate` forbids the `columns` field.
The `ignore.where` contract is a WHERE expression without the keyword, not a complete SQL statement.

## Columns and strategies

Each column rule requires `name` and `strategy`.
Optional `params` carries strategy parameters; `consistent`, `onNull` and `onEmpty` override their inherited behavior.
`onNull` and `onEmpty` accept `Keep` or `Generate`.
`consistent: true` requests equal output for equal input across tables within the Run; this execution behavior is not implemented in M1.

The strategy enum is:

```text
firstName lastName fullName jobTitle email phone date address
streetAddress streetSuffix city postalCode state country company
companySuffix vatId iban ipv4Public url uuid alphanumeric digits
number text constant hash mask null
```

| Parameter | Schema contract |
| --- | --- |
| `locale`, `domain` | Optional locale and email-domain overrides. |
| `ascii` | Optional boolean; defaults to `true`. |
| `digits` | Defaults to `15`; maximum `15`. |
| `from`, `to` | Date bounds; `from` defaults to `1950-01-01`. |
| `format` | Optional `date` or `datetime`. |
| `country` | Defaults to `DE`. |
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
The schema rejects unknown strategies and enum values; it does not implement the strategies or every strategy-specific runtime constraint.
An omitted date upper bound and hash length have execution defaults of today and 64 respectively, rather than CRD defaults.

## Status contract

`status.hash` is reserved for the SHA-256 of canonical policy JSON.
The `Valid` condition uses reasons `SpecValid`, `StepRefMissing` or `PatternInvalid`.
These fields are not populated by M1 because the Policy controller is inactive.
The short name is `apol`.
