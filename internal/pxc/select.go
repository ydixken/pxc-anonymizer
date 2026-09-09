package pxc

type BackupSelection struct {
	Backup            *BackupView
	Succeeded         int
	MissingCompletion int
}

// Creation time cannot establish when a backup completed.
// Missing completion timestamps remain visible to the caller instead of becoming candidates.
func LatestSucceeded(backups []BackupView) BackupSelection {
	var selection BackupSelection
	for i := range backups {
		candidate := &backups[i]
		if !candidate.Succeeded() {
			continue
		}
		selection.Succeeded++
		if candidate.Status.CompletedAt == nil || candidate.Status.CompletedAt.IsZero() {
			selection.MissingCompletion++
			continue
		}
		if selection.Backup == nil || newerBackup(candidate, selection.Backup) {
			selection.Backup = candidate
		}
	}
	return selection
}

func newerBackup(candidate, current *BackupView) bool {
	if order := candidate.Status.CompletedAt.Compare(current.Status.CompletedAt.Time); order != 0 {
		return order > 0
	}
	if order := candidate.CreationTimestamp.Compare(current.CreationTimestamp.Time); order != 0 {
		return order > 0
	}
	return candidate.Name < current.Name
}
