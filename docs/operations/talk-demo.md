# Present the pipeline in three to four minutes

Use a prepared development environment and a completed full-pipeline rehearsal to show a fresh source backup, temporary restore, anonymization, new output backup and downstream restore.
The `scripts/demo-session.sh` launcher opens four read-only k9s views as native iTerm2 panes using `tmux -CC`.
It starts monitoring only.

We start the full rehearsal before the speaking slot because the source backup, temporary restore, anonymization, output backup and downstream restore can exceed four minutes.
Choose a start time from your rehearsal measurements and keep the completed full-chain evidence available as fallback.
A recorded Run without a downstream Bootstrap result covers only the middle of this demonstration.
Label the selected Run as live or recorded whenever you switch evidence.
A short speaking slot is not an end-to-end completion guarantee.

## Run the prepared private entrypoints

The prepared kit keeps its context, namespaces and resource choices in ignored `tasks/demo/` scripts.
Run these entrypoints without arguments; the reusable public helpers below remain configurable.
The pipeline uses all five operator kinds: BackupPointer publishes the source, Policy defines the transformation, Schedule creates a Run, the Run restores and anonymizes a temporary PXC, and Bootstrap restores the published output downstream.
A Schedule does not create Bootstrap automatically; the pipeline entrypoint coordinates that final handoff.

> [!warning]
> The pipeline creates backups and a temporary database, then restores the selected downstream database.
> Prepare the dedicated target, credentials, source data and capacity before running it.
> Opening the watcher or the iTerm2 session performs no setup mutations.

1. Seed the dedicated source only when preparing its fixture.
   The existing `seed-demo` command runs in a fresh Job.
   A matching marker preserves the seeded rows; a mismatched or partly populated fixture fails closed.

   ```sh
   tasks/demo/01-seed-db.sh
   ```

2. Start the reviewed full pipeline before the speaking slot.
   It starts with a fresh source backup and uses the prepared Schedule for the new Run.
   Keep its stage messages visible in the presenter terminal; a failed prerequisite or wait needs inspection before proceeding.
   A completed standalone Run is not a substitute for a downstream restore.

   ```sh
   tasks/demo/02-apply-manifests.sh
   ```

3. Watch the whole lifecycle from another terminal.
   The private watcher prints source, anonymization and downstream snapshots every two seconds using native `kubectl get`.
   It shows the relevant operator and Percona resources in each namespace, including the temporary PXC and both restore boundaries.
   Stop with Ctrl-C; empty lists or a displayed phase do not constitute a success check.

   ```sh
   tasks/demo/03-watch-resources.sh
   ```

4. Open the four native panes from an ordinary iTerm2 tab outside tmux.
   The prefilled session wrapper starts monitoring only.

   ```sh
   tasks/demo/00-session.sh
   ```

## Reuse the standalone helpers

The three public scripts under `scripts/demo/` are independent commands.
`01-seed-db.sh` prepares the source fixture, `02-apply-manifests.sh` coordinates the full backup-to-downstream flow, and `03-watch-resources.sh` monitors one resource kind.
None of them is run automatically by the native iTerm2 launcher.

Check out these **examples** for preparing another environment:

- Seed a dedicated source using the existing image's `seed-demo` entrypoint and the namespace's root Secret.
  Replace these invented references in your private wrapper when preparing another environment.
  The helper follows the demo seed Job's default TLS-disabled connection; use the [seed-demo CLI](../configuration/seed-demo.md#tls) for verified TLS configuration.

  ```sh
  bash scripts/demo/01-seed-db.sh \
    --context example-development --namespace source-demo \
    --host example-source-haproxy.source-demo.svc.cluster.local \
    --secret example-source-users \
    --image ghcr.io/ydixken/pxc-anonymizer:v0.1.0-alpha.5 --scale 1
  ```

- Run the full pipeline with explicit source, operator and downstream configuration.
  The public helper takes `--source-backup`, `--source-pointer`, `--schedule` and `--bootstrap` alongside its context and namespace flags.
  It requires local JSON backup and Bootstrap templates and `kubectl`, `jq`, `shasum` and `mktemp` on PATH.
  Use one reviewed Bootstrap target, an explicit selector with pause/resume enabled, and no recreation.
  Set `pxc-anonymizer.io/demo-crossplane-count` to the expected positive resource count.
  Include defaulted fields explicitly so a stored Bootstrap matches the template during a guarded trigger update.
  The helper never applies the whole examples directory.
  Use the prefilled private entrypoint for the prepared environment, or inspect the public interface when preparing another one.

  ```sh
  bash scripts/demo/02-apply-manifests.sh --help
  ```

- Watch one kind or one named object with native `kubectl get --watch`.
  Start each watcher in its own terminal; use `perconaxtradbclusterbackups` in the source namespace or `bootstraps` in the downstream namespace for other boundaries.
  A comma-separated group of kinds is intentionally rejected because this helper runs one watch stream.

  ```sh
  bash scripts/demo/03-watch-resources.sh \
    --context example-development --namespace database-demo anonymizationruns
  ```

The fresh seed Job projects only Secret key `root`, runs as a nonroot user without an API token, and keeps the password out of command arguments and local files.
Its database-readiness wait is 30 minutes, its Job deadline is 35 minutes, and the client waits at most 36 minutes for Complete.
It has no automatic retry and a one-hour TTL after finishing.
That TTL belongs only to the fresh manually created Job; an existing completed GitOps seed Job is left intact.
A failed Job can remain visible until the bounded client wait ends; inspect its status before choosing another attempt.
A timeout or failed seed does not trigger backup creation, another seed, or resource deletion.

## Prepare the environment and evidence

1. Prepare the [installation](../installation.md), [synthetic source data](../configuration/seed-demo.md) and [pipeline inputs](../planning.md) in an authorized development context.
   Prepare the Policy, source BackupPointer, enabled Schedule template and same-namespace credentials before the rehearsal.
   The pipeline must observe a fresh successful source backup and its published pointer before requesting the Schedule-owned Run.
   For separate source and Run namespaces, use `spec.source.pointer.http.url`; `backupPointerRef` resolves only within the Run's namespace.
   Adapt [example 03](../examples/03-run-from-pointer.yaml) using the HTTP source and output pointer fields in [example 09](../examples/09-reference.yaml).
   Output publication requires `spec.output.pointer`; an output backup alone does not imply a published pointer.
   Keep the adapted manifest and exact operational names in ignored private notes.

2. Install iTerm2, tmux and k9s through your usual tool-management process and check their help locally.
   The separate preparation and verification commands also use kubectl, jq and curl.
   The launcher uses native tmux argument execution and k9s context, namespace, initial-resource and read-only flags.
   Its flags were checked against tmux 3.7c and k9s 0.51.0.
   See the [tmux manual](https://man.openbsd.org/tmux.1) and [k9s command reference](https://k9scli.io/topics/commands/).

   ```sh
   tmux -V
   k9s version
   k9s help
   bash scripts/demo-session.sh --help
   ```

3. Select the context and namespaces in a separate presenter terminal.
   Replace these invented names with your approved development mapping.
   `OPERATOR_NAMESPACE` means the namespace containing the Runs and temporary PXC resources; the manager Deployment may live elsewhere.

   ```sh
   DEV_CONTEXT=example-development
   SOURCE_NAMESPACE=source-demo
   OPERATOR_NAMESPACE=pipeline-demo
   DOWNSTREAM_NAMESPACE=downstream-demo
   RUN_NAME=example-rehearsal
   kubectl --context "$DEV_CONTEXT" config view --minify \
     -o jsonpath='{.current-context}{"\n"}'
   kubectl --context "$DEV_CONTEXT" -n "$OPERATOR_NAMESPACE" \
     get anonymizationrun "$RUN_NAME"
   ```

4. Verify that your read permissions include the namespaced resources and API discovery needed by k9s.
   In k9s, use `Ctrl-a` to inspect discovered resource aliases if a view is unavailable.
   The relevant views are `perconaxtradbclusters`, `perconaxtradbclusterbackups`, `backuppointers`, `anonymizationpolicies`, `anonymizationruns`, `anonymizationschedules`, `bootstraps`, `perconaxtradbclusterrestores`, `pods`, `jobs`, `pvc` and `events`.
   k9s accepts discovered singular names, plural names and short names; see its [resource command support](https://k9scli.io/topics/commands/) and [aliases documentation](https://k9scli.io/topics/aliases/).
   An empty view is not proof that a controller or CRD is ready.

5. Rehearse the full source-to-downstream chain and retain selected status evidence outside version control.
   Capture the selected source backup, Schedule-owned Run and downstream Bootstrap identities.
   Retain the Run name, UID, generation, phases, condition reasons, attempts, table/row progress and output identity, then the Bootstrap trigger, target and restore result.
   Prepare a reviewed before/after example using synthetic data through your normal database access, with credentials kept off screen.
   Do not claim a particular row transformation from a condition alone.
   Keep any screenshots or recordings free of private hosts, account identifiers, credentials and real personal data before sharing them.

   ```sh
   DEMO_EVIDENCE_DIR=tasks/talk-evidence
   mkdir -p "$DEMO_EVIDENCE_DIR"
   kubectl --context "$DEV_CONTEXT" -n "$OPERATOR_NAMESPACE" \
     get anonymizationrun "$RUN_NAME" \
     -o jsonpath='{.metadata.name}{"\t"}{.metadata.uid}{"\t"}{.metadata.generation}{"\n"}{.status.phase}{"\n"}{.status.startedAt}{"\n"}{.status.completedAt}{"\n"}{.status.anonymize.progress}{"\n"}{.status.anonymize.attempts}{"\n"}{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.observedGeneration}{"\n"}{end}' \
     > "$DEMO_EVIDENCE_DIR/run-status.txt"
   test -s "$DEMO_EVIDENCE_DIR/run-status.txt"
   ```

6. Prepare the downstream Bootstrap target and restore prerequisites as part of the same rehearsal.
   Use [example 07](../examples/07-bootstrap-minimal.yaml) and the [Bootstrap procedure](bootstrap.md), or [example 08](../examples/08-bootstrap-crossplane.yaml) when Crossplane coordination is required.
   Bootstrap restores existing PXC targets and replaces their data.
   Require the selected downstream target and credential mapping to be ready before starting the pipeline.
   If no downstream completion has been verified, present that stage as unverified and do not describe the rehearsal as end to end.
   Keep an application-user login check separate from controller completion.

## Launch and navigate

1. Start monitoring from a regular iTerm2 tab outside any tmux session, ideally around 200 columns by 60 rows.
   The launcher attaches with `tmux -CC attach-session`, which lets iTerm2 render the native panes.
   Keep the k9s context and namespace headers visible and maximize the pane you are presenting when the projector is smaller.

   ```sh
   bash scripts/demo-session.sh \
     --context "$DEV_CONTEXT" \
     --source-namespace "$SOURCE_NAMESPACE" \
     --operator-namespace "$OPERATOR_NAMESPACE" \
     --downstream-namespace "$DOWNSTREAM_NAMESPACE"
   ```

2. Prepare the monitor without attaching when useful, then attach from a presenter terminal.
   Rerunning with the same inputs reuses the session without replacing panes.
   A different configuration, an unrelated session with the same name or an incomplete setup returns an error; select a different `--session` name.

   ```sh
   bash scripts/demo-session.sh \
     --context "$DEV_CONTEXT" \
     --source-namespace "$SOURCE_NAMESPACE" \
     --operator-namespace "$OPERATOR_NAMESPACE" \
     --downstream-namespace "$DOWNSTREAM_NAMESPACE" \
     --session pxc-talk --detach
   tmux -CC attach-session -t =pxc-talk
   ```

The launcher uses this native layout and initially focuses the Run pane:

| Position | Initial view | Switch here during narration |
| --- | --- | --- |
| Top left | Source backups | PXC and BackupPointer. |
| Top right | AnonymizationRuns | Policy and temporary PXC. |
| Bottom left | Pipeline events | Restore, Jobs, pods and PVCs. |
| Bottom right | Bootstraps | Downstream PXC and Restore. |

Use native iTerm2 controls for this control-mode session.
The originating tab becomes the tmux control menu, while the monitoring window contains the k9s panes.
Use the menu actions below so personal key mappings do not change the instructions.
See the [iTerm2 integration guide](https://iterm2.com/documentation-tmux-integration.html) and [menu reference](https://iterm2.com/documentation-menu-items.html).

| Native action | Purpose |
| --- | --- |
| Click a pane | Focus the resource view you want to present. |
| View > Maximize Active Pane | Expand that pane; repeat to restore the grid. |
| Drag a pane divider | Adjust the split sizes. |
| Resize the iTerm2 window | Adjust the available presentation area. |
| Shell > tmux > Dashboard | Find the tmux monitoring window. |
| Shell > tmux > Detach | Disconnect while the tmux session persists. |
| Escape in the originating control-menu tab | Detach cleanly. |

Keep normal k9s keys inside the monitoring panes; tmux prefix shortcuts are not needed for native navigation.
Avoid closing a monitoring pane/tab/window to detach because native close actions can kill the corresponding tmux resource.
Return with the same launcher command or `tmux -CC attach-session -t =pxc-talk` from an ordinary iTerm2 tab.
The launcher refuses interactive use outside iTerm2 or from inside tmux; `--detach` remains available for preparation.
Native control-mode protocol and pane creation were checked with inert processes; the iTerm2 graphical interface and live k9s connectivity were not exercised by those checks.

In k9s, type `:anonymizationruns` and Enter to change resource view, `/name` to filter, `d` to describe a selected resource and Escape to return.
Use `?` for the current view's keys.
Keep the context unchanged during the talk.
Read-only mode disables k9s modification commands; it does not replace Kubernetes RBAC or remove sensitive content from readable resources.
Do not open Secret payloads or unreviewed logs on screen.

The launcher's success message confirms a tmux session was prepared, not cluster connectivity or pipeline success.
A k9s process that exits leaves its pane for inspection.
An authorization error, missing CRD or empty list needs rehearsal attention before presenting.

## Speaking cues for a 3:45 slot

1. **0:00–0:25, source:** Show the fresh Percona backup of the seeded source and the BackupPointer that selected it.
   Say: "We start with a physical source backup; the pointer publishes that exact input for the pipeline."
   Distinguish Ready from Fresh and identify the selected backup, not just an older successful row.

2. **0:25–0:45, Policy and Schedule:** Show the Policy rule, the manual Schedule request and its owned Run.
   Say: "The Schedule starts one execution with this Policy; the Run freezes its inputs."
   Label the execution as live or recorded and state when it started.

3. **0:45–1:30, temporary restore:** Show the Run's `Provisioning` and `Restoring` evidence, its temporary PXC and its Percona Restore.
   Say: "We spawn an isolated database and restore the source backup into it."
   The source PXC remains the backup source, not the anonymization target.
   Use captured child evidence when cleanup has already removed the live resources.

4. **1:30–2:05, anonymization:** Show the `Anonymizing` stage and recorded table/row progress and attempt count.
   Say: "The runner applies the frozen Policy to the temporary restore."
   A reviewed synthetic before/after example demonstrates the data change; a condition alone does not show transformed rows.

5. **2:05–2:45, new output backup:** Show the new Percona output backup, `BackedUp` and `Published` conditions, and the pointer identity check.
   Say: "We back up the anonymized database and publish that result for the downstream restore."
   Match the pointer to this Run and output backup before claiming that handoff succeeded.

6. **2:45–3:30, downstream restore:** Show Bootstrap, its recorded target PXC and its Percona Restore.
   Say: "Bootstrap restores this published output into the existing downstream target."
   Require phase `Completed`, a current-generation `Complete=True` condition and an observed trigger matching the request before presenting that operation as finished.
   If it is still active or only configured, say so; an earlier completed Run does not prove this stage.
   Keep an application-user login claim separate from controller completion.

7. **3:30–3:45, result:** Return to the overview and state the actual outcome of all stages.
   Identify which observations are live, which came from rehearsal, and any remaining work.
   If the pipeline remains active, name its stage and let it continue outside the speaking slot.

## Follow the selected execution

The private pipeline entrypoint coordinates a fresh source backup, one manual Schedule request, its owned Run and a downstream Bootstrap.
Do not create a separate direct Run alongside that request.
The Schedule must already be deployed with the reviewed template, a current Ready condition and the intended concurrency policy.
Suspension blocks manual requests; Forbid also blocks while a previous Run is active or cleaning up.
Keep the manual request token out of Git.

The source handoff requires a successful new backup and a current BackupPointer selecting it.
Run source status records backup name, destination and timestamps, not the Percona Backup UID.
Retain the actual Backup UID separately and compare the frozen source with the intended new backup.
The HTTP input has no atomic expected-source pin; a mismatched selection must stop the handoff.
The next handoff requires the selected Run's completed anonymization, successful output backup and published pointer identifying that Run.
Bootstrap uses the downstream namespace's credentials and restores the reviewed existing target.
Require phase `Completed`, a current-generation `Complete=True` condition and the matching observed trigger before accepting the Bootstrap result.
The private kit uses a separate script-managed Bootstrap with the selected Run UID as trigger, so a Git-managed object does not revert that trigger.
A successful script rehearsal is separate from GitOps rollout acceptance.
Use the [Schedule guide](../configuration/schedule.md), [Bootstrap procedure](bootstrap.md) and [condition reference](../reference/conditions.md) when inspecting a stopped stage.

Run the private watcher or use these k9s views while the pipeline terminal performs its bounded waits:

| Stage | Views |
| --- | --- |
| Fresh source | PXC, backups, BackupPointer. |
| Execution request | Policy, Schedule, Run. |
| Temporary database | PXC and Restore. |
| Anonymization | Run progress and conditions. |
| Output | Backup and Run publication. |
| Downstream | Bootstrap, Restore and PXC. |

The watcher deliberately repeats snapshots because the [native get command](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_get/) supports multiple resource kinds in one listing.
Its output contains resource summaries, not Secret payloads or pod logs.
A watcher error stops that watcher; it does not cancel or retry pipeline work.
Use the pipeline's selected identities when several Runs or backups are visible.

## Decide what the evidence proves

| Observation | What to say |
| --- | --- |
| Source backup `Succeeded` | The selected physical source backup completed. |
| BackupPointer Ready and Fresh | Publication and source age are separate checks. |
| Policy Valid | Policy validation passed, not every runtime check. |
| Run phase and conditions | These are the recorded stages of this execution. |
| Percona Restore `Succeeded` | Restore succeeded; `Starting Cluster` is still active. |
| Run `Anonymized=True` | The runner reported success for its frozen Policy. |
| Output backup and `Published=True` | The backup and configured publication succeeded. |
| Run `Complete=True` | The controller finished its pipeline and cleanup. |
| Bootstrap `Complete=True` | Restore and configured coordination completed. |
| Successful application login | Separate proof of the application's access path. |

Match condition `observedGeneration` to the resource generation and check Failed as well as earlier successful stages.
Schedule Ready is not proof that its latest Run succeeded.
For a published pointer, verify the configured HTTP endpoint during rehearsal as well as inspecting the controller condition.
Missing samples, missing children or empty lists are not successful checks by themselves.

## Finish, reset or fall back

Detach through iTerm2 Shell > tmux > Detach after the talk.
Detaching does not stop a Run, suspend a Schedule, delete storage or modify a Bootstrap.
Use another `--session` name when a fresh monitor layout is needed; the launcher never kills an existing session.

Temporary clusters and copied credentials have the [Run cleanup lifecycle](lifecycle.md).
Run-owned snapshots stay until Run deletion; owned Jobs follow the configured TTL.
Output backups survive Run deletion, and stored pointer objects are not removed by Kubernetes garbage collection.
PVC or volume release and retained completed Jobs may still need separate observation.
Follow the [temporary-cluster inspection procedure](temp-cluster.md#inspect-the-recorded-cluster) using recorded child identities.
Do not promise that every resource or byte of storage is gone from one condition.

For another rehearsal, inspect the prior Schedule request, Run, Bootstrap and retained artifacts before choosing a new reviewed execution identity.
Follow the [normal deletion procedure](lifecycle.md#delete-through-the-run) only when deletion is intended and authorized.
Do not delete generated children, strip finalizers, force storage removal or recreate a missing recorded Restore to make the presentation look successful.
Keep the Schedule's reviewed suspension state consistent with its Git owner.
Changing a completed Bootstrap's trigger starts another restore and requires the same target review as the first execution.

If a live operation stalls, say: "The live execution is waiting in this phase; these next results are from the rehearsal."
Show its reason or Event if it is safe to display, then switch to the labeled rehearsal evidence without creating another Run.
A local wait timeout does not prove controller failure.
If the API or terminal is unavailable, use the captured Run evidence and the reviewed manifest walkthrough.
If there is no completed rehearsal for a stage, describe the configured behavior and leave its live outcome unverified.
