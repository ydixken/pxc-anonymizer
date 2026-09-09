/*
Copyright 2026 pxc-anonymizer contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// Condition reasons are stable strings consumed by automation and status readers.
const (
	ReasonLatestSucceeded        = "LatestSucceeded"
	ReasonNoSucceededBackup      = "NoSucceededBackup"
	ReasonNewerBackupInProgress  = "NewerBackupInProgress"
	ReasonSelectedBackupDeleted  = "SelectedBackupDeleted"
	ReasonNewerBackupFailed      = "NewerBackupFailed"
	ReasonUploaded               = "Uploaded"
	ReasonVerified               = "Verified"
	ReasonUploadFailed           = "UploadFailed"
	ReasonCredentialsInvalid     = "CredentialsInvalid"
	ReasonEndpointUnreachable    = "EndpointUnreachable"
	ReasonCredentialsUnavailable = "CredentialsUnavailable"
	ReasonWithinStaleAfter       = "WithinStaleAfter"
	ReasonStaleBackup            = "StaleBackup"
	ReasonPointerCurrent         = "PointerCurrent"
	ReasonSuspended              = "Suspended"
	ReasonDangling               = "Dangling"
	ReasonSpecValid              = "SpecValid"
	ReasonStepRefMissing         = "StepRefMissing"
	ReasonPatternInvalid         = "PatternInvalid"
	ReasonParsed                 = "Parsed"
	ReasonInvalidCron            = "InvalidCron"
	ReasonScheduled              = "Scheduled"
	ReasonBlocked                = "Blocked"
	ReasonPointerUnavailable     = "PointerUnavailable"
	ReasonRestoreVanished        = "RestoreVanished"
	ReasonClusterUnpaused        = "ClusterUnpaused"
	ReasonTooManyMissedTimes     = "TooManyMissedTimes"
	ReasonFromBackupPointer      = "FromBackupPointer"
	ReasonFromPointer            = "FromPointer"
	ReasonFromBackup             = "FromBackup"
	ReasonLiteral                = "Literal"
	ReasonPointerNotReady        = "PointerNotReady"
	ReasonPointerFetchFailed     = "PointerFetchFailed"
	ReasonBackupNotSucceeded     = "BackupNotSucceeded"
	ReasonInvalidDestination     = "InvalidDestination"
	ReasonSnapshotted            = "Snapshotted"
	ReasonPolicyNotFound         = "PolicyNotFound"
	ReasonPolicyInvalid          = "PolicyInvalid"
	ReasonReady                  = "Ready"
	ReasonProvisioning           = "Provisioning"
	ReasonTimeout                = "Timeout"
	ReasonClusterError           = "ClusterError"
	ReasonSucceeded              = "Succeeded"
	ReasonRestoring              = "Restoring"
	ReasonRestoreFailed          = "RestoreFailed"
	ReasonDryRun                 = "DryRun"
	ReasonRunning                = "Running"
	ReasonRunnerFailed           = "RunnerFailed"
	ReasonPolicyError            = "PolicyError"
	ReasonSchemaMismatch         = "SchemaMismatch"
	ReasonPermissionDenied       = "PermissionDenied"
	ReasonUniqueViolation        = "UniqueViolation"
	ReasonRetryBudgetExhausted   = "RetryBudgetExhausted"
	ReasonBackupFailed           = "BackupFailed"
	ReasonBackupDeadlineExceeded = "BackupDeadlineExceeded"
	ReasonSkipped                = "Skipped"
	ReasonTempClusterDeleted     = "TempClusterDeleted"
	ReasonRetained               = "Retained"
	ReasonDeleting               = "Deleting"
)
