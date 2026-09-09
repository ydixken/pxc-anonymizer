# Third-party CRDs

We vendor the three CRDs from the [Percona operator chart 1.20.0](https://github.com/percona/percona-helm-charts/blob/pxc-operator-1.20.0/charts/pxc-operator/crds/crd.yaml) and the MySQL Database, User and Grant CRDs from [provider-sql v0.13.0](https://github.com/crossplane-contrib/provider-sql/tree/v0.13.0/package/crds).
These upstream schemas let local controller tests serve the foreign APIs without importing either operator's Go module.
The vendor script splits the Percona document stream without changing schema bytes and copies each provider-sql file unchanged.

Run these commands from the repository root with Python 3.
Regeneration also needs curl and HTTPS access to GitHub's public Contents API.
Each download has a 30-second deadline and an 8 MiB size limit, and must match the reviewed source digest before any files are written.

1. Fetch the pinned schemas into this directory.

   ```sh
   hack/vendor-crds.sh
   ```

2. Check the six files against their pinned identities and SHA256 digests offline without writing files.

   ```sh
   hack/vendor-crds.sh --check
   ```

We keep the check offline so upstream availability and rate limits cannot affect local validation.
The check fails on missing, changed, duplicate or unexpected CRDs.
Unexpected YAML files are reported and left unchanged.
For an upstream upgrade, independently retrieve and review the new tagged source bytes before updating the two tags and digest constants in `hack/vendor-crds.sh`.
The Percona source digest covers the entire chart stream; its three output digests cover each extracted document, and each provider-sql digest covers the unchanged source file.
Then regenerate and review the schema diff.
