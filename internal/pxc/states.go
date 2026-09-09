package pxc

type BackupState string

// Match the upstream wire values, including the empty initial state.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/apis/pxc/v1/pxc_backup_types.go#L177-L186
const (
	BackupNew       BackupState = ""
	BackupSuspended BackupState = "Suspended"
	BackupStarting  BackupState = "Starting"
	BackupRunning   BackupState = "Running"
	BackupFailed    BackupState = "Failed"
	BackupSucceeded BackupState = "Succeeded"
)

type RestoreState string

// Match all restore phases so intermediate and future states cannot imply success.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/apis/pxc/v1/pxc_prestore_types.go#L78-L90
const (
	RestoreNew            RestoreState = ""
	RestoreStarting       RestoreState = "Starting"
	RestoreStopCluster    RestoreState = "Stopping Cluster"
	RestoreRestore        RestoreState = "Restoring"
	RestoreStartCluster   RestoreState = "Starting Cluster"
	RestorePITR           RestoreState = "Point-in-time recovering"
	RestorePrepareCluster RestoreState = "Preparing Cluster"
	RestoreFailed         RestoreState = "Failed"
	RestoreSucceeded      RestoreState = "Succeeded"
)

type ClusterState string

// Cluster states use lowercase wire values, unlike backup and restore states.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/apis/pxc/v1/pxc_types.go#L403-L411
const (
	ClusterInitializing ClusterState = "initializing"
	ClusterPaused       ClusterState = "paused"
	ClusterStopping     ClusterState = "stopping"
	ClusterReady        ClusterState = "ready"
	ClusterError        ClusterState = "error"
)

func (b BackupView) Succeeded() bool {
	return b.Status.State == BackupSucceeded
}

// The operator continues finalization only for these two terminal backup states.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/controller/pxcbackup/controller.go#L144-L154
func (b BackupView) IsTerminal() bool {
	return b.Status.State == BackupSucceeded || b.Status.State == BackupFailed
}

func (r RestoreView) Succeeded() bool {
	return r.Status.State == RestoreSucceeded
}

// Starting Cluster still needs reconciliation and is never terminal.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/controller/pxcrestore/controller.go#L109-L115
func (r RestoreView) IsTerminal() bool {
	return r.Status.State == RestoreSucceeded || r.Status.State == RestoreFailed
}

// A missing replica count must not make zero ready replicas look healthy.
// https://github.com/percona/percona-xtradb-cluster-operator/blob/v1.20.0/pkg/apis/pxc/v1/pxc_types.go#L465-L470
func (c ClusterView) Ready() bool {
	return !c.Spec.Pause && c.Status.State == ClusterReady && c.Spec.PXC.Size > 0 && c.Status.PXC.Ready == c.Spec.PXC.Size
}
