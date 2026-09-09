// Package metrics records observed BackupPointer state in the manager registry.
// Unknown timestamps and unobserved candidate counts remain absent.
// Source age derives from the completion timestamp; Ready=False exposes dangling state.
// ForgetBackupPointer removes every series when the resource disappears.
package metrics
