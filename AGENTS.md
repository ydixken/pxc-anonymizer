# AGENTS.md

We build a Go Kubernetes operator for anonymizing Percona XtraDB Cluster backups.
This guide applies to agents and human contributors.
Interpret MUST, MUST NOT, SHOULD, SHOULD NOT and MAY as defined by RFC 2119.

[TOC]

## Public repository and authorization

> [!warning]
> This repository is public.
> Private customer names, infrastructure hosts, account URLs, bucket names, secret names, context names and cluster inventory MUST NOT enter code, documentation, logs, commits, issues or pull requests.
> Secret values and personal data MUST NOT be printed or committed.

Use invented examples and `example.com` hosts in public material.
Keep operational identifiers, reconnaissance and environment details in private ops notes.
Agents MAY inspect or update development secrets only within the user's explicit authorization and MUST keep their values out of output.

Agents MAY push feature branches, open pull requests and merge green pull requests within this project's authorized development scope.
Confirm the scope from the user's instructions and private ops notes before touching an external system.
Stop before an action leaves that scope or requires destructive changes beyond the user's authorization.
Do not request confirmation again for actions the user already authorized.

Agents MUST use only the explicitly authorized development Kubernetes context.
Production contexts are off-limits to agents, including for reads.
Do not run a cluster command until its context matches the authorized context from private ops notes.
Never bypass a task's context prompt with `task --yes`.
Use an unattended task only when its expected-context precondition verifies the intended context.

## Branches and task scope

Use a feature branch in the primary checkout.
Verify that the branch is neither `main`, `master` nor `dev` before implementation.
Agents MUST NOT create or use Git worktrees, push directly to protected branches or overwrite another contributor's work.
Keep commits small and conventional.
Do not add agent attribution or session URLs to commits, pull requests or project files.

Follow the approved package's read, write and acceptance boundaries.
An approved plan takes precedence over earlier design notes.
Record adjacent work in the private task list instead of expanding scope.
Re-plan a package after two failed attempts instead of retrying it unchanged.
Coordinate independent work through focused agents with explicit file ownership and independent verification.

## Mandatory skills

- Apply [ponytail](.claude/skills/ponytail/SKILL.md) first at level `full` for coding, review, design and dependency choices.
- Apply [brainstorming](.claude/skills/brainstorming/SKILL.md) before designing new features or changing behavior.

Existing design approval and the user's authorization remain valid across handoffs.
Repository instructions take precedence over conflicting instructions in a vendored skill.

## The solution ladder

Stop at the first option that solves the actual problem:

1. Remove an unnecessary requirement.
2. Reuse an existing helper or pattern.
3. Use the standard library or a framework's existing option.
4. Use a dependency already in the manifest.
5. Use one focused function or call.
6. Write the minimum new code needed.

Never remove validation, error handling, security or correctness to shorten a change.
Trace the affected flow before choosing a solution.
Add no dependency without validating compatibility, licensing and security.
Prefer bounded polling over fixed sleeps.

## Structure and generated files

We use Kubebuilder's Go layout, API group `pxc-anonymizer.io` and version `v1alpha1`.
The manager entry is `cmd/main.go`; API types live in `api/v1alpha1/` and controllers in `internal/controller/`.
Keep integrations behind focused package boundaries and externalize configuration.
Use structured logging without sensitive values.

Kubebuilder owns the scaffold and its markers.
Regenerate `config/` and DeepCopy files instead of editing generated output.
The Makefile's permitted adaptations cover version stamping, module-derived envtest versions and versioned tool installation.
Build-configuration tests protect those changes.
The Taskfile is the human and agent entry point and delegates Go checks to Make.

## Tests, documentation and verification

Every behavior change MUST include meaningful tests and update affected documentation in the same change.
Use table tests for pure logic and envtest for controller behavior.
Do not treat missing inputs, skipped checks or zero matches as successful verification.

1. Format the touched source files.
2. Run the local lint checks.

   ```sh
   task lint
   ```

3. Run the local test suite.

   ```sh
   task test
   ```

Both targets MUST pass before every commit.
Run the package's additional acceptance commands and retain their real output in the private task record.
A passing CI run supplements local verification.
Do not claim completion without command output proving it.

The secrecy guard checks staged content, tracked working files and untracked files that Git does not ignore.
It requires the tracked digest deny-list and the private `tasks/secrecy-deny.regex` file.
Only `CI=true` may skip the private regex layer; the digest layer always runs.
Keep both lists nonempty and maintain private patterns through private ops notes.

## Writing conventions

Write one sentence per Markdown line.
Use imperative instructions and "we" for team decisions.
Keep the README an index and put substantive guides in `docs/`.
Use callouts for warnings and numbered steps for procedures.
Avoid em dashes, marketing language and status claims that become stale.
Comments explain why a choice exists, not what the code already does.
Keep comments within two lines for a variable, three for a function and six for a file header unless essential information has no other home.

## Sources of truth

Start with the [README](README.md), this guide and the approved private plan.
Consult the relevant upstream documentation before relying on unfamiliar behavior.
Use public fixtures from the authorized development environment, never production reconnaissance.
