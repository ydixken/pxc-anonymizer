# Demo data

`seed-demo` creates a deterministic MySQL dataset in `demo_users`, `demo_orders` and `demo_content`.
We keep the fixture profile fixed as `demo-v1` so repeated invocations can recognize a completed seed.

> [!warning]
> Use a dedicated demo server with no application data in these schemas.
> Confirm the host and credentials before running the command: it creates schemas, tables and data.

## Run the seed

1. Build the executable from the repository root and inspect the seed command's options.

   ```sh
   make build
   bin/manager seed-demo --help
   ```

2. Confirm the target server and place its password in a file outside the checkout.
   The command reads the file's raw bytes without trimming a trailing newline.
   Replace the example host and password-file path with your dedicated demo target.

   ```sh
   bin/manager seed-demo \
     --host=127.0.0.1 \
     --port=3306 \
     --user=root \
     --password-file=/path/to/mysql-password \
     --wait=30m \
     --scale=1
   ```

`--wait` bounds the readiness wait.
`--scale` is an integer from 1 through 10 and defaults to 1.
There is no profile flag.

## Dataset

At scale 1, the fixture contains these rows:

| Table | Rows |
| --- | ---: |
| `demo_users.user` | 5000 |
| `demo_users.organisation` | 1000 |
| `demo_users.address` | 8000 |
| `demo_users.email_blacklist` | 300 |
| `demo_orders.commission` | 10000 |
| `demo_orders.commission_identity` | 10000 |
| `demo_orders.commission_details_message` | 3000 |
| `demo_orders.audit_log` | 2 |
| `demo_content.message_property` | 50 |
| `demo_users.seed_meta` | 1 |

Scale multiplies the row counts except `audit_log`, which stays at two rows, and `seed_meta`, which stays at one.
Related rows share deterministic values for testing consistent transformations across tables.
The audit fixture has a composite primary key with two rows for user 1 and event IDs 1 and 2.
The content fixture includes a `robots.txt` value for SQL post-step examples.

## Repeat runs and failures

The final `demo_users.seed_meta` row records `demo-v1`, the scale and the completion timestamp.
A matching marker skips seeding and preserves both the data and timestamp.
A mismatched profile or scale fails without replacing the fixture.
An absent marker with any nonempty fixture table also fails.

Schema DDL runs before the data transaction and can remain after a failure.
All seed rows and the final marker commit atomically on one connection.
The session recursion limit is set to 1000000 for the generated dataset.

## TLS

`seed-demo` defaults to TLS disabled to support the demo seed Job.
Set `--tls-mode=verify-full` to verify the server certificate and hostname, and add `--tls-ca-file=/path/to/ca.pem` for a custom CA.
Verified mode does not fall back to plaintext.
The `anonymize` command retains its `verify-full` default.
