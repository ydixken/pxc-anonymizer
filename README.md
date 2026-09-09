# pxc-anonymizer

pxc-anonymizer is a Kubernetes operator for preparing Percona XtraDB Cluster backup workflows for development and testing.
The M1 release publishes BackupPointers and installs five validated API contracts; the anonymization, scheduling and restore controllers follow in later releases.

[TOC]

## Features

1. **Backup pointers:** publish the latest successful PXC backup as reusable JSON, with separate readiness and freshness conditions.
2. **Five API contracts:** BackupPointer, AnonymizationPolicy, AnonymizationRun, AnonymizationSchedule and Bootstrap.
3. **Explicit storage access:** same-namespace credential references and configurable S3 endpoints.
4. **Local checks:** focused Go and admission tests, strict linting and publication safeguards.

## Structure

```sh
api/v1alpha1/         # API source and generated DeepCopy methods
cmd/                  # Manager entrypoint
internal/             # Controller, storage and PXC support
config/               # Generated CRDs and Kubernetes configuration
charts/pxc-anonymizer/ # Installable Helm chart
docs/                 # Installation, API contracts and examples
hack/                 # Generation and publication safeguards
test/                 # Build checks and API fixtures
Taskfile.yml          # Local command entrypoint
Makefile              # Generation, build and test tooling
Dockerfile            # Manager image build
```

## Getting Started

1. Install Go matching [go.mod](go.mod), Git, Make and the tools needed by the checks you run.
   Confirm the Go toolchain before building.

   ```sh
   go version
   ```

2. Build the manager from this checkout.

   ```sh
   make build
   ```

For a development-cluster installation, follow the [Installation guide](docs/installation.md), then the [BackupPointer quickstart](docs/quickstart.md).
The installation guide makes you select and inspect the target context before changing cluster resources.

## Commands

| Command | Purpose |
| --- | --- |
| `task help` | List available tasks. |
| `task lint` | Check secrecy, YAML, Go, chart consistency and workflows. |
| `task test` | Run Go, admission and build configuration tests. |
| `make build` | Generate code and compile `bin/manager`. |

Local verification requires Task v3, yamllint, Python 3, Helm, actionlint and kubeconform in addition to the Go build tools.
Local lint requires the maintainers' ignored `tasks/secrecy-deny.regex` file.
Keep this private policy out of version control.
E2E is a small milestone check, separate from routine lint and tests.

## Installation

See the [Installation guide](docs/installation.md).

## Quickstart

See the [BackupPointer quickstart](docs/quickstart.md).

## API

See the [API reference](docs/reference/api.md).

## Contributing

See the [contributor and agent guidance](AGENTS.md).

## License

See the [Apache 2.0 license](LICENSE).
Vendored skills retain their included licenses.
