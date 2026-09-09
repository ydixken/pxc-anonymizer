# Pointer documents

We publish additive schema-v2 JSON while accepting legacy documents containing `name` and `destination`.
An absent `schemaVersion` decodes as 1, and unknown fields are ignored.
Encoding always writes version 2.
Optional metadata retains publication time and identity, source cluster, backup details, source S3 location, anonymization identity and the public URL.
Credentials never belong in a document.

Use `Fetcher` with an uncached Kubernetes reader and the resource namespace.
HTTP sources require HTTPS unless `allowInsecure` is explicit, verify server certificates, support a same-namespace CA Secret, and reject insecure redirects unless permitted.
Their default timeout is 30 seconds and default response limit is 65,536 bytes.
S3 JSON reads and writes share the 65,536-byte limit.

Use `objectstore.Resolve` for namespace-local credentials and optional key remapping.
Kubernetes has already decoded Secret data; the resolver uses those bytes directly.
S3 endpoints cannot contain a path, credentials, query or fragment.
An unset addressing style uses SDK auto-selection; explicit true uses path style and explicit false uses DNS style.
Region defaults to `auto`.

`PutJSON` sets `Content-Type: application/json` and `Cache-Control: no-cache`.
Passing a `Document` also sets the backup-name object metadata.
`Head` distinguishes a missing object through `Found=false`; `GetJSON` returns `objectstore.ErrNotFound`.
Invalid credentials and unreachable endpoints have separate error sentinels.
Missing Secrets remain recognizable with Kubernetes `IsNotFound`.

## MinIO service test

`TestMinIOPointerRoundTrip` checks a stored schema-v2 pointer through PUT, HEAD and GET, including `application/json`, `no-cache` and the ETag.
Use an existing MinIO bucket with permission to read, write and delete objects under `service-test/objectstore/`.
The test uses path-style requests and verifies HTTPS certificates with system trust.

1. Supply `PXC_ANONYMIZER_TEST_S3_ENDPOINT` as an absolute HTTP or HTTPS origin and `PXC_ANONYMIZER_TEST_S3_BUCKET` as the existing bucket name.
   Supply credentials through `PXC_ANONYMIZER_TEST_S3_ACCESS_KEY_ID` and `PXC_ANONYMIZER_TEST_S3_SECRET_ACCESS_KEY` in the process environment.
   Set `PXC_ANONYMIZER_TEST_S3_REGION` if the service uses a region other than the test default `us-east-1`.
2. Run from the repository root:

   ```sh
   go test ./internal/objectstore -run '^TestMinIOPointerRoundTrip$' -count=1 -v
   ```

An unset endpoint skips this service test.
Once configured, missing inputs, unreachable storage, failed assertions and cleanup errors fail the test.
The test writes one random object key, deletes only that key even after a failed upload response, and confirms its absence with HEAD.
Storage operations have a 30-second deadline, and cleanup has a separate 30-second deadline.
