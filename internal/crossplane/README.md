# Crossplane coordination

Create a client with the configured API group and version, then select the configured plural kinds in order.
Managed resources are cluster-scoped; the selector is the isolation boundary.
An empty selection is an error.

Use the Bootstrap UID as the pause owner token.
Persist every reference returned by `Pause`, including partial results returned with an error.
Its atomic owner annotation lets a retry recover a pause whose status write failed.
Pass only these durable references to `Resume`; objects paused by other owners are excluded.
Call `Ready` for the exact expected set after resumption and require an empty pending list with no error.
Missing objects, replacement UIDs, and either missing readiness condition remain pending.

Persist the initial `Recreate` result before calling it again: that first call performs no mutation.
Keep the original references and API group/version fixed throughout the operation.
Persist subsequent progress even when a call returns an error.
Recreation uses original-UID delete preconditions and never deletes a replacement UID.
Kinds advance in selection order after the preceding kind completes.
The default waits for the replacement to have both `Ready=True` and `Synced=True`.
Disabling that wait still requires observing removal of the original UID.
Database recreation requires `deletionPolicy: Orphan`, and finalizer removal requires explicit opt-in.

The caller owns durable Bootstrap status writes, ten-second polling, readiness timeout, and reporting the first ten pending names.
The helper performs one reconciliation step without sleeping or creating resources.
