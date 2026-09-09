# pxc-anonymizer

pxc-anonymizer is a Go project for a Kubernetes operator that prepares anonymized Percona XtraDB Cluster backups for development and testing.
The foundation provides a Kubebuilder manager scaffold and local build, lint, test and publication checks.

[TOC]

## Features

1. **Manager scaffold:** a Go entrypoint and generated Kubernetes configuration.
2. **Local checks:** Go tests, build configuration tests and strict YAML and Go linting.
3. **Publication safeguards:** a secrecy guard and exclusions for private working files.

## Structure

```sh
cmd/                # Manager entrypoint
config/             # Generated Kubebuilder configuration
hack/               # Generation boilerplate and secrecy guard
test/buildconfig/   # Build and publication regression tests
Taskfile.yml        # Local check entrypoint
Makefile            # Kubebuilder generation, build and test tooling
Dockerfile          # Manager image build
PROJECT             # Kubebuilder project metadata
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
| `task test` | Run Go and build configuration tests. |
| `make build` | Generate code and compile `bin/manager`. |

## Contributing

See the [contributor and agent guidance](AGENTS.md).

## License

See the [Apache 2.0 license](LICENSE).
Vendored skills retain their included licenses.
