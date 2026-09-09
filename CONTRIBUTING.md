# Contributing

Read [AGENTS.md](AGENTS.md) before changing code or documentation.
Use a feature branch and keep tests and affected documentation in the same change.

## Local development

Use Go matching [go.mod](go.mod), Bash, Git and Make.
Repository checks also use Task v3, yamllint, Python 3, Helm, actionlint and kubeconform.
Local lint requires the maintainers' ignored `tasks/secrecy-deny.regex`; obtain that policy through the maintainers and keep it out of version control.

1. Build the executable from the repository root.

   ```sh
   make build
   bin/manager manager --help
   ```

2. Run the repository checks before committing.

   ```sh
   task lint
   task test
   ```

Routine lint and tests do not run the live E2E workflow.
Configured service tests require their documented service inputs; leave those variables unset for ordinary local unit checks.
Use conventional commit messages and include the commands and results in the pull request.

## Documentation

Keep README an index and put guides in `docs/`.
Use one sentence per line and page-relative Markdown links.
API field documentation is generated from `api/v1alpha1`; update the Go comments rather than editing the generated reference.

1. Install the pinned MkDocs Material release in an isolated Python environment.

   ```sh
   DOCS_VENV="$(mktemp -d)"
   python3 -m venv "$DOCS_VENV"
   . "$DOCS_VENV/bin/activate"
   python -m pip install mkdocs-material==9.7.7
   ```

2. Regenerate the reference and build the site in the same shell.

   ```sh
   task docs
   ```

The docs task runs `crd-ref-docs` v0.3.0 through Go, then requires a strict build and valid local links and anchors.
Its site output goes to `bin/docs-site`.
Do not commit generated site output or private operational notes.

## Examples and credentials

Use synthetic names and data in public examples.
Keep credentials in files or the designated secret-management system, never in manifests, command arguments, logs or screenshots.
Confirm the target environment before any operation that creates resources or changes data.
