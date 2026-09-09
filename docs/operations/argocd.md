# Argo CD health checks

Use these checks to expose the five resource kinds in Argo CD without treating an unobserved spec or an intermediate pipeline stage as success.
The [conditions reference](../reference/conditions.md) explains the observations behind each result.

## Install the customizations

Merge the five entries below into `data` in the configuration that manages your Argo CD installation's `argocd-cm` ConfigMap.
Keep its existing keys and namespace; each block is one data entry, not a complete ConfigMap manifest.
If another operator manages Argo CD, put the customizations in that operator's supported configuration instead of editing its generated ConfigMap.
These keys use the [Argo CD custom-health interface](https://argo-cd.readthedocs.io/en/release-3.5/operator-manual/health/#way-1-define-a-custom-health-check-in-argocd-cm-configmap), also documented in [v3.5.2](https://github.com/argoproj/argo-cd/blob/v3.5.2/docs/operator-manual/health.md).
The scripts use the supplied `obj` and need no `useOpenLibs` setting.

Publish the configuration through the workflow that owns Argo CD, then inspect each resource's health and message in its Application.
The operator chart does not install these customizations.
Resource health, Git synchronization and the latest sync operation's outcome are separate observations.
A Healthy resource does not prove that an earlier failed sync operation succeeded.

## Health policy

We require a condition's `observedGeneration` to match `metadata.generation` before using it.
For every kind except Policy, we also require the status-level observed generation to match.
Missing or stale observations remain Progressing; an absent Kubernetes object is handled by Argo CD itself.
Deletion remains Progressing until the resource disappears, without claiming that finalizers or compensation have finished.
Requested suspension on BackupPointer or Schedule returns Suspended; this reports the requested state, not proof that every child has stopped.

| Kind | Healthy requires | Degraded observation |
| --- | --- | --- |
| BackupPointer | Ready and Fresh | Stale backup, error or dangling pointer |
| AnonymizationPolicy | Valid | Invalid rules or references |
| AnonymizationRun | Completed phase and Complete | Failed condition or phase |
| AnonymizationSchedule | ScheduleValid and Ready | Invalid schedule |
| Bootstrap | Matching trigger, Completed and Complete | Failed condition or phase |

We include backup freshness in BackupPointer health: a published but stale backup is Degraded even when Ready remains True.
Schedule health describes scheduling availability; inspect each child Run for execution success.
Run and Bootstrap failures take precedence over retained successful stage conditions.
Publication alone cannot make either pipeline Healthy, and optional stages need not have conditions when skipped.

## BackupPointer

```yaml
resource.customizations.health.pxc-anonymizer.io_BackupPointer: |
  local metadata = obj.metadata or {}
  local status = obj.status or {}
  local result = {status = "Progressing", message = "Waiting for current-generation status"}
  if metadata.deletionTimestamp ~= nil then
    result.message = "Deletion requested; waiting for resource removal"
    return result
  end
  if (obj.spec or {}).suspend == true then
    return {status = "Suspended", message = "Pointer reconciliation is suspended"}
  end
  local generation = metadata.generation
  if generation == nil or status.observedGeneration ~= generation then
    return result
  end
  local conditions = {}
  for _, condition in ipairs(status.conditions or {}) do
    if condition.observedGeneration == generation then
      conditions[condition.type] = condition
    end
  end
  local ready = conditions.Ready
  local fresh = conditions.Fresh
  if fresh ~= nil and fresh.status == "False" then
    return {status = "Degraded", message = "No published backup is within staleAfter"}
  end
  if status.phase == "Error" or status.phase == "Dangling" then
    return {status = "Degraded", message = status.phase}
  end
  if ready ~= nil and ready.status == "True" and fresh ~= nil and fresh.status == "True" then
    return {status = "Healthy", message = "Pointer is ready and its backup is fresh"}
  end
  result.message = status.phase or "Waiting for Ready and Fresh"
  return result
```

## AnonymizationPolicy

Policy has no status-level `observedGeneration`; only its Valid condition supplies that check.
Valid does not establish database connectivity or compatibility with the restored schema.

```yaml
resource.customizations.health.pxc-anonymizer.io_AnonymizationPolicy: |
  local metadata = obj.metadata or {}
  local status = obj.status or {}
  local result = {status = "Progressing", message = "Waiting for current-generation validation"}
  if metadata.deletionTimestamp ~= nil then
    result.message = "Deletion requested; waiting for resource removal"
    return result
  end
  local generation = metadata.generation
  if generation == nil then
    return result
  end
  for _, condition in ipairs(status.conditions or {}) do
    if condition.type == "Valid" and condition.observedGeneration == generation then
      if condition.status == "False" then
        return {status = "Degraded", message = condition.reason or "Policy is invalid"}
      end
      if condition.status == "True" then
        return {status = "Healthy", message = "Policy rules and references are valid"}
      end
    end
  end
  return result
```

## AnonymizationRun

A failed Run stays Degraded while cleanup or failure retention settles.
A successful dry run can become Healthy without creating a backup or publishing a pointer.

```yaml
resource.customizations.health.pxc-anonymizer.io_AnonymizationRun: |
  local metadata = obj.metadata or {}
  local status = obj.status or {}
  local result = {status = "Progressing", message = "Waiting for current-generation status"}
  if metadata.deletionTimestamp ~= nil then
    result.message = "Deletion requested; waiting for finalizers and resource removal"
    return result
  end
  local generation = metadata.generation
  if generation == nil or status.observedGeneration ~= generation then
    return result
  end
  local conditions = {}
  for _, condition in ipairs(status.conditions or {}) do
    if condition.observedGeneration == generation then
      conditions[condition.type] = condition
    end
  end
  local failed = conditions.Failed
  if failed ~= nil and failed.status == "True" then
    return {status = "Degraded", message = failed.reason or "Run failed"}
  end
  if status.phase == "Failed" then
    return {status = "Degraded", message = "Run failed; inspect its conditions"}
  end
  local complete = conditions.Complete
  if status.phase == "Completed" and complete ~= nil and complete.status == "True" then
    return {status = "Healthy", message = "Run and temporary resource cleanup completed"}
  end
  result.message = status.phase or "Waiting for Complete"
  return result
```

## AnonymizationSchedule

A concurrency block is Progressing, not a failed Run.
Suspension prevents scheduling but does not cancel an already active Run.

```yaml
resource.customizations.health.pxc-anonymizer.io_AnonymizationSchedule: |
  local metadata = obj.metadata or {}
  local status = obj.status or {}
  local result = {status = "Progressing", message = "Waiting for current-generation scheduling status"}
  if metadata.deletionTimestamp ~= nil then
    result.message = "Deletion requested; waiting for resource removal"
    return result
  end
  if (obj.spec or {}).suspend == true then
    return {status = "Suspended", message = "Scheduling is suspended"}
  end
  local generation = metadata.generation
  if generation == nil or status.observedGeneration ~= generation then
    return result
  end
  local conditions = {}
  for _, condition in ipairs(status.conditions or {}) do
    if condition.observedGeneration == generation then
      conditions[condition.type] = condition
    end
  end
  local valid = conditions.ScheduleValid
  local ready = conditions.Ready
  if valid ~= nil and valid.status == "False" then
    return {status = "Degraded", message = valid.reason or "Schedule is invalid"}
  end
  if valid ~= nil and valid.status == "True" and ready ~= nil and ready.status == "True" then
    return {status = "Healthy", message = "Schedule is available to create Runs"}
  end
  result.message = (ready or {}).reason or "Waiting for ScheduleValid and Ready"
  return result
```

## Bootstrap

The requested trigger must match `status.observedTrigger` before an earlier execution can affect health.
Failed compensation does not turn a failed execution into a successful one.

```yaml
resource.customizations.health.pxc-anonymizer.io_Bootstrap: |
  local metadata = obj.metadata or {}
  local status = obj.status or {}
  local result = {status = "Progressing", message = "Waiting for current-generation status"}
  if metadata.deletionTimestamp ~= nil then
    result.message = "Deletion requested; waiting for compensation and resource removal"
    return result
  end
  local generation = metadata.generation
  if generation == nil or status.observedGeneration ~= generation then
    return result
  end
  if (status.observedTrigger or "") ~= ((obj.spec or {}).trigger or "") then
    result.message = "Waiting for the requested trigger"
    return result
  end
  local conditions = {}
  for _, condition in ipairs(status.conditions or {}) do
    if condition.observedGeneration == generation then
      conditions[condition.type] = condition
    end
  end
  local failed = conditions.Failed
  if failed ~= nil and failed.status == "True" then
    return {status = "Degraded", message = failed.reason or "Bootstrap failed"}
  end
  if status.phase == "Failed" then
    return {status = "Degraded", message = "Bootstrap failed; inspect its conditions"}
  end
  local complete = conditions.Complete
  if status.phase == "Completed" and complete ~= nil and complete.status == "True" then
    return {status = "Healthy", message = "Bootstrap completed for the requested trigger"}
  end
  result.message = status.phase or "Waiting for Complete"
  return result
```

## Keep GitOps ownership separate

Declare the five operator resources in Git; let the operator create and reconcile its temporary clusters, runner Jobs and restore CRs.
Do not add those generated objects to another Application's desired manifests or attach Argo tracking metadata that makes that Application manage their deletion.
Temporary resources and restores have controller owner references.
Output backups deliberately lack a Run owner reference so retained backups can survive Run cleanup; leave their deletion to the documented retention policy.

> [!warning]
> Pruning or replacing a recorded restore can produce `RestoreVanished` and an execution failure.
> Recreating a same-named object does not restore its original identity or authorize an automatic restore replay.

Use the [lifecycle guide](lifecycle.md) for retries and deletion, and the [Bootstrap runbook](bootstrap.md) for compensation and Crossplane ownership.
These health scripts observe the CR's status only; they do not contact MySQL, object storage or child resources.
