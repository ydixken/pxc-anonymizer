# Strategies

Set a column's `strategy` to one canonical name below and put its arguments in `params`.
See the generated [ColumnRule](api.md#columnrule) and [StrategyParams](api.md#strategyparams) fields for admission constraints, and the [Policy guide](../configuration/policy.md) for complete rules and Secret references.
Runtime validation also checks parameter combinations and the actual database schema before execution.

## Shared controls

`onNull` and `onEmpty` inherit the Policy defaults, which are `Keep`.
Set either to `Generate` to replace those inputs; these checks run before every strategy, including `constant`, `mask` and `null`.
An empty string therefore stays empty under the default `null` rule unless `onEmpty: Generate` is set.

`consistent` defaults to true except for `alphanumeric`, `digits`, `number` and `text`, which default to row-based generation.
`constant`, `mask` and `null` reject the `consistent` field, including explicit false.
For the remaining strategies, set `consistent: true` or `false` to override the strategy default.

Applicable name, job, address and geography strategies accept only `locale: en`, inherited from `defaults.locale` when omitted.
Their entries identify this argument explicitly.
`vatId` and `iban` accept only `country: DE`.
Other values fail runtime validation even though the API's locale and country fields are free strings.

## Determinism

We derive generation keys with HMAC-SHA256 from the seed and canonical Policy hash.
`PerRun` uses a separate 32-byte seed for each Run; `Fixed` reads at least 32 raw bytes from the referenced Secret.
Each Run snapshots those inputs, and each generated value owns its random-number source, so worker scheduling does not change its output.
Changing the Policy hash or seed changes the mapping.

Consistent generation keys on the strategy and normalized input: surrounding whitespace is trimmed, Unicode is normalized to NFC, and email input is lowercased.
Equal inputs need the same strategy, parameters and key to produce equal outputs across tables.
Row-based generation keys on database, table, paging-key value and column, rather than the original value.
It remains repeatable for the same row identity and key.
`constant`, `mask` and `null` follow their explicit replacement rules without a random source.

Database UNIQUE collisions permit at most eight deterministic attempts, numbered 0 through 7, for the collided row.
Collision retries can give equal original inputs different final replacements.
Choose sufficient output space and preserve the required database constraints.
For `date`, an omitted upper bound uses the Run's frozen creation date; set `to` explicitly for identical Fixed-mode outputs across Runs created on different days.

## SQL target constraints

String strategies require `CHAR`, `VARCHAR`, a TEXT-family column, `BINARY`, `VARBINARY` or a BLOB-family column.
`number`, `date`, `constant` and `null` have the additional target forms described in their entries.
For non-null strategies, JSON, ENUM, SET and BIT columns are outside these supported target families.
Text capacity is checked in Unicode characters; binary and BLOB capacity is checked in bytes.

Known fixed output lengths must fit before any write, including the default hash lengths.
Constant values and numeric bounds are also validated before writes.
Variable-width generated values are checked individually and can fail a page when a value exceeds its column capacity.
Use an InnoDB base table with a primary key, or an explicitly selected complete unique index of non-null columns.
Generated columns and mutations of the paging key are rejected.
See the [Run preflight contract](../configuration/run.md#runner-timeouts-and-cleanup) for execution and rollback limits.

## firstName

Generates a person's first name as a string.
Argument: `locale`, default `en`; only `en` is supported.

## lastName

Generates a person's last name as a string.
Argument: `locale`, default `en`; only `en` is supported.

## fullName

Generates a person's full name as a string.
Argument: `locale`, default `en`; only `en` is supported.

## jobTitle

Generates a job title as a string.
Argument: `locale`, default `en`; only `en` is supported.

## email

Generates an email string with a lowercase ASCII local part and a deterministic suffix.
`domain` inherits `defaults.emailDomain`; when both are absent, the generator supplies a domain.
`ascii` defaults to true and requires an ASCII custom domain; explicit false permits a valid Unicode domain while the local part remains ASCII.
Domain validation rejects address separators, whitespace and malformed addresses.
Reserve at least 32 characters, and ensure each complete generated address fits its actual capacity.

## phone

Generates a decimal-digit string, preserving leading zeroes.
`digits` defaults to 15 and must be between 1 and 15; output length is exactly that value.
Use a string or binary target to preserve the digits as generated.

## date

Generates a UTC date or datetime string between date bounds.
`from` defaults to `1950-01-01`; optional `to` uses the frozen Run creation date when omitted.
Both explicit bounds use `YYYY-MM-DD`, and the upper bound must not precede the lower bound.
`format` defaults to `date` (`YYYY-MM-DD`, 10 characters); `datetime` emits `YYYY-MM-DD HH:MM:SS` (19 characters).
Targets may be string/binary columns or DATE, DATETIME and TIMESTAMP; a DATE target requires the date format.

## address

Generates a string combining street, city, state and postal code.
Argument: `locale`, default `en`; only `en` is supported.

## streetAddress

Generates a street-address string.
Argument: `locale`, default `en`; only `en` is supported.

## streetSuffix

Generates a street-suffix string.
Argument: `locale`, default `en`; only `en` is supported.

## city

Generates a city-name string.
Argument: `locale`, default `en`; only `en` is supported.

## postalCode

Generates a postal-code string.
Argument: `locale`, default `en`; only `en` is supported.

## state

Generates a state-name string.
Argument: `locale`, default `en`; only `en` is supported.

## country

Generates a country-name string.
Argument: `locale`, default `en`; only `en` is supported.
The `country` parameter used by VAT and IBAN does not select this strategy's output.

## company

Generates a company-name string.
There are no strategy-specific arguments.

## companySuffix

Generates a company-suffix string.
There are no strategy-specific arguments.

## vatId

Generates `DE` followed by nine decimal digits, for an 11-character string.
`country` defaults to `DE`; only `DE` is supported.
This produces a synthetic identifier, without validating a tax registration.

## iban

Generates a 22-character German IBAN string with a mod-97 check value.
`country` defaults to `DE`; only `DE` is supported.
The generated value does not identify a verified bank account.

## ipv4Public

Generates an IPv4 string excluding the generator's private and reserved address ranges.
There are no strategy-specific arguments; an address has at most 15 characters.
The result carries no reachability guarantee.

## url

Generates a URL string.
There are no strategy-specific arguments; the email `domain` setting does not control URL generation.

## uuid

Generates a 36-character UUID string.
There are no strategy-specific arguments.

## alphanumeric

Generates a string of letters and decimal digits.
`length` is required and must be between 1 and 4096.
`case` defaults to `Mixed`; `Upper` and `Lower` restrict the letter case.
Output length is exactly `length`; generation defaults to row-based.

## digits

Generates an exactly sized decimal-digit string, padded with leading zeroes.
`length` is required and must be between 1 and 4096; generation defaults to row-based.
The API accepts `unique`, but the runtime adds no separate uniqueness check or database constraint for that flag.
Actual UNIQUE violations use the shared bounded collision retries.

## number

Generates an integer in the inclusive range from `min` to `max`, defaulting to 0 through 100.
Both bounds are signed 64-bit integers and `min` must not exceed `max`; generation defaults to row-based.
Targets may be string/binary columns, TINYINT, SMALLINT, MEDIUMINT, INT, BIGINT, DECIMAL, FLOAT or DOUBLE.
The entire configured range must fit the target's signedness, integer range, decimal precision/scale or floating-point range.

## text

Generates sentence text, then truncates the result to at most `maxLength` Unicode characters.
`maxLength` defaults to 255 and accepts 1 through 4096; `sentences` defaults to 1 and accepts 1 through 100.
Generation defaults to row-based.

## constant

Returns the exact string from `value` or a same-namespace Secret `valueFrom` selector.
Runtime validation requires exactly one source, and `valueFrom` needs both name and key.
An explicit empty `value` is valid; inherited NULL/empty handling still applies before replacement.
Targets may be string/binary columns or the numeric and temporal families supported by `number` and `date`, provided the value parses and fits the target.
Sensitive constants remain in the Run's immutable Secret snapshot and projected files.
The `consistent` field is forbidden.

## hash

Generates the HMAC-SHA256 value used by deterministic generation as an encoded string.
`encoding` defaults to `hex`, whose default and maximum `length` is 64; standard `base64` defaults to and allows at most 44 characters.
An explicit `length` must be at least 1; shorter lengths truncate the encoding, and longer lengths are rejected.
Generation defaults to consistent, so this is keyed pseudonymization rather than an unkeyed input digest.

## mask

Preserves `keepPrefix` and `keepSuffix` Unicode characters and replaces the middle with `maskChar`.
Both counts default to 0 and accept 0 through 4096; `maskChar` defaults to `*` and must be one Unicode character.
If the retained counts cover the input, the original string is returned.
The string keeps its character count; a multibyte mask can increase its byte length in a binary target.
The `consistent` field is forbidden.

## null

Replaces generated inputs with SQL NULL and requires a nullable target column.
There are no strategy-specific arguments, and the `consistent` field is forbidden.
Use `onEmpty: Generate` when empty strings must also become NULL.

## Migration spellings

Policy rules use the canonical names above.
The migration parser translates supported legacy spellings, such as `first_name` to `firstName` and `uuid4` to `uuid`; see the [coverage mapping](coverage.md).
Its positional length syntax is limited to `alphanumeric(n)`, `digits(n)` and their mapped legacy names, with `n` from 1 through 4096.
For example, `alphanumeric_upper_case(12)` maps to `alphanumeric` with `length: 12` and `case: Upper`, while `random_number(8)` maps to `digits` with `length: 8`.
Use those canonical fields in a Policy; the runtime does not automatically translate legacy spellings during admission or execution.
