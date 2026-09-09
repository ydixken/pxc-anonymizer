//nolint:goconst // Literal upstream states catch accidental changes to production constants.
package pxc

import "testing"

func TestBackupStates(t *testing.T) {
	for _, state := range []BackupState{"", "Suspended", "Starting", "Running", "Failed", "Succeeded", "unknown", "FutureState", "succeeded"} {
		t.Run(string(state), func(t *testing.T) {
			view := BackupView{Status: BackupStatus{State: state}}
			if got, want := view.IsTerminal(), state == "Failed" || state == "Succeeded"; got != want {
				t.Fatalf("terminal = %v, want %v", got, want)
			}
			if got, want := view.Succeeded(), state == "Succeeded"; got != want {
				t.Fatalf("succeeded = %v, want %v", got, want)
			}
		})
	}
}

func TestRestoreStates(t *testing.T) {
	for _, state := range []RestoreState{
		"", "Starting", "Stopping Cluster", "Restoring", "Starting Cluster", "Point-in-time recovering",
		"Preparing Cluster", "Failed", "Succeeded", "unknown", "FutureState", "succeeded",
	} {
		t.Run(string(state), func(t *testing.T) {
			view := RestoreView{Status: RestoreStatus{State: state}}
			if got, want := view.IsTerminal(), state == "Failed" || state == "Succeeded"; got != want {
				t.Fatalf("terminal = %v, want %v", got, want)
			}
			if got, want := view.Succeeded(), state == "Succeeded"; got != want {
				t.Fatalf("succeeded = %v, want %v", got, want)
			}
		})
	}
}

func TestClusterReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state ClusterState
		size  int32
		ready int32
		pause bool
		want  bool
	}{
		{name: "ready", state: "ready", size: 3, ready: 3, want: true},
		{name: "zero size", state: "ready"},
		{name: "missing status", size: 3},
		{name: "initializing", state: "initializing", size: 3, ready: 3},
		{name: "replicas pending", state: "ready", size: 3, ready: 2},
		{name: "paused", state: "ready", size: 3, ready: 3, pause: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := ClusterView{Spec: ClusterSpec{Pause: tc.pause, PXC: ClusterPXCSpec{Size: tc.size}},
				Status: ClusterStatus{State: tc.state, PXC: ClusterPXCStatus{Ready: tc.ready}}}
			if got := view.Ready(); got != tc.want {
				t.Fatalf("ready = %v, want %v", got, tc.want)
			}
		})
	}
}
