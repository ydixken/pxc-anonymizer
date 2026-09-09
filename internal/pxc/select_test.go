//nolint:goconst // Selection cases use literal timestamps and states as independent inputs.
package pxc

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func completedBackup(t *testing.T, name, completed, created string) BackupView {
	t.Helper()
	view := BackupView{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: BackupStatus{State: BackupSucceeded}}
	if completed != "" {
		parsed, err := time.Parse(time.RFC3339, completed)
		if err != nil {
			t.Fatal(err)
		}
		timestamp := metav1.NewTime(parsed)
		view.Status.CompletedAt = &timestamp
	}
	if created != "" {
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			t.Fatal(err)
		}
		view.CreationTimestamp = metav1.NewTime(parsed)
	}
	return view
}

func TestLatestSucceeded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backups    []BackupView
		want       string
		succeeded  int
		incomplete int
	}{
		{name: "empty"},
		{name: "missing completion", backups: []BackupView{completedBackup(t, "missing", "", "2026-01-01T00:00:00Z")},
			succeeded: 1, incomplete: 1},
		{name: "zero completion", backups: []BackupView{{Status: BackupStatus{State: BackupSucceeded, CompletedAt: &metav1.Time{}}}},
			succeeded: 1, incomplete: 1},
		{name: "typed offset ordering", backups: []BackupView{
			completedBackup(t, "earlier", "2026-01-01T12:00:00+02:00", ""),
			completedBackup(t, "later", "2026-01-01T11:00:00Z", ""),
		}, want: "later", succeeded: 2},
		{name: "creation tie break", backups: []BackupView{
			completedBackup(t, "older", "2026-01-01T12:00:00Z", "2026-01-01T09:00:00Z"),
			completedBackup(t, "newer", "2026-01-01T12:00:00Z", "2026-01-01T10:00:00Z"),
		}, want: "newer", succeeded: 2},
		{name: "name tie break", backups: []BackupView{
			completedBackup(t, "b", "2026-01-01T12:00:00Z", ""), completedBackup(t, "a", "2026-01-01T12:00:00Z", ""),
		}, want: "a", succeeded: 2},
		{name: "other states ignored", backups: []BackupView{
			{Status: BackupStatus{State: BackupRunning}}, {Status: BackupStatus{State: BackupFailed}},
			{Status: BackupStatus{State: "unknown"}}, completedBackup(t, "complete", "2026-01-01T12:00:00Z", ""),
			completedBackup(t, "missing", "", "2026-01-02T00:00:00Z"),
		}, want: "complete", succeeded: 2, incomplete: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]BackupView(nil), tc.backups...)
			selection := LatestSucceeded(tc.backups)
			got := ""
			if selection.Backup != nil {
				got = selection.Backup.Name
			}
			if got != tc.want || selection.Succeeded != tc.succeeded || selection.MissingCompletion != tc.incomplete {
				t.Fatalf("selection = %+v (name %q), want %q, succeeded=%d, missing=%d", selection, got, tc.want, tc.succeeded, tc.incomplete)
			}
			if !reflect.DeepEqual(before, tc.backups) {
				t.Fatal("selection changed input order or values")
			}
		})
	}
}
