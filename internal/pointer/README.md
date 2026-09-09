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
