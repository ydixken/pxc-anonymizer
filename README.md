# pxc-anonymizer

pxc-anonymizer is a Go project for a Kubernetes operator that prepares anonymized Percona XtraDB Cluster backups for development and testing.
The scaffold defines five API types and provides controller skeletons, generation tooling and local verification.

[TOC]

## Features

1. **API scaffold:** `BackupPointer`, `AnonymizationPolicy`, `AnonymizationRun`, `AnonymizationSchedule` and `Bootstrap`.
2. **Local checks:** Go and envtest suites, build configuration tests and strict YAML and Go linting.
3. **Publication safeguards:** a secrecy guard and exclusions for private working files.

API types and CRDs are generated together.
We add controller registrations with their implementations.

## Structure

```sh
api/v1alpha1/         # API types and generated DeepCopy methods
cmd/                  # Manager entrypoint
internal/controller/  # Controller skeletons and envtest suite
config/               # Generated Kubebuilder configuration
hack/                 # Generation boilerplate and secrecy guard
test/buildconfig/     # Build and publication regression tests
Taskfile.yml          # Local check entrypoint
Makefile              # Kubebuilder generation, build and test tooling
Dockerfile            # Manager image build
PROJECT               # Kubebuilder project metadata
```

## Getting Started

1. Install Go matching [go.mod](go.mod), Bash, Git, Make, Task v3, yamllint and Python 3.
   Confirm the tools are available.

   ```sh
   go version
   task --version
   yamllint --version
   python3 --version
   ```

2. Build `bin/manager`.

   ```sh
   make build
   ```

3. Obtain the private secrecy patterns from the maintainers and place them in the ignored `tasks/secrecy-deny.regex` file.
   Keep the patterns out of version control.
   Confirm the file is nonempty and ignored.

   ```sh
   test -s tasks/secrecy-deny.regex
   git check-ignore tasks/secrecy-deny.regex
   ```

4. Run the local checks.

   ```sh
   task lint
   task test
   ```

## Commands

| Command | Purpose |
| --- | --- |
| `task help` | List local tasks. |
| `task lint` | Check secrecy, YAML and Go. |
| `task test` | Run Go, envtest and build configuration tests. |
| `make build` | Generate code and compile `bin/manager`. |

## Contributing

See the [contributor and agent guidance](AGENTS.md).

## License

See the [Apache 2.0 license](LICENSE).
Vendored skills retain their included licenses.
