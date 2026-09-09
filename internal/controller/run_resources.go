// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
	"github.com/ydixken/pxc-anonymizer/internal/pointer"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
	runnerpolicy "github.com/ydixken/pxc-anonymizer/internal/runner/policy"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

const (
	runContainerName = "anonymize"
	runOutputStorage = "anonymized"
	runFixedSeedMode = "Fixed"
	runPolicyVolume  = "policy"
	runAWSAccessKey  = "AWS_ACCESS_KEY_ID"
	runAWSSecretKey  = "AWS_SECRET_ACCESS_KEY"

	runPolicyFile    = "policy.json"
	runSpecFile      = "run.json"
	runSeedKey       = "seed"
	runSourceFile    = "source.json"
	runPolicyHashKey = "policy.hash"
)

var runSystemKeys = []string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication", "xtrabackup-aes256-psk"}

type runValidationError struct{ condition, reason, message string }

func (e *runValidationError) Error() string { return e.message }

type runSnapshot struct {
	Spec         api.AnonymizationRunSpec
	Policy       api.AnonymizationPolicySpec
	Source       api.ResolvedSource
	SourceReason string
	OutputGroup  string
	PolicyHash   string
	SystemUsers  *corev1.Secret
	Payload      *corev1.Secret
}

type runSourceSnapshot struct {
	Source      api.ResolvedSource `json:"source"`
	Reason      string             `json:"reason"`
	OutputGroup string             `json:"outputGroup"`
}

func invalidRun(condition, reason, message string) error {
	return &runValidationError{condition: condition, reason: reason, message: message}
}

func runOwner(run *api.AnonymizationRun) metav1.OwnerReference {
	return *metav1.NewControllerRef(run, api.GroupVersion.WithKind("AnonymizationRun"))
}

func runOwns(run *api.AnonymizationRun, object client.Object) error {
	owner := metav1.GetControllerOf(object)
	if object.GetNamespace() != run.Namespace || owner == nil || owner.UID != run.UID ||
		owner.Kind != "AnonymizationRun" || owner.Name != run.Name || owner.APIVersion != api.GroupVersion.String() {
		return fmt.Errorf("refusing foreign child %T %s", object, object.GetName())
	}
	return nil
}

func (r *AnonymizationRunReconciler) createRunChild(ctx context.Context, run *api.AnonymizationRun, desired client.Object) error {
	if !controllerutil.ContainsFinalizer(run, runFinalizer) {
		return errors.New("run finalizer must precede child creation")
	}
	if err := runOwns(run, desired); err != nil {
		return err
	}
	labels := desired.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["pxc-anonymizer.io/run-uid"] = string(run.UID)
	desired.SetLabels(labels)
	reader := client.Reader(r.Client)
	if _, secret := desired.(*corev1.Secret); secret {
		reader = r.APIReader
	}
	if reader == nil {
		return errors.New("uncached APIReader is required")
	}
	current := desired.DeepCopyObject().(client.Object)
	err := reader.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired, client.FieldOwner("pxc-anonymizer")); err == nil {
			return nil
		} else if !apierrors.IsAlreadyExists(err) {
			return err
		}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(desired), current); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := runOwns(run, current); err != nil {
		return err
	}
	if !current.GetDeletionTimestamp().IsZero() {
		return errors.New("owned child is still deleting")
	}
	return nil
}

func runTempName(run *api.AnonymizationRun) string {
	prefix := run.Spec.TempCluster.NamePrefix
	if prefix == "" {
		prefix = "anon"
	}
	if len(prefix) > 13 {
		prefix = prefix[:13]
	}
	prefix = strings.TrimRight(prefix, "-")
	sum := sha256.Sum256([]byte(run.UID))
	return prefix + "-" + hex.EncodeToString(sum[:4])
}

func runChildName(run *api.AnonymizationRun, suffix string) string {
	name := run.Name
	if len(name)+len(suffix)+1 > 63 {
		sum := sha256.Sum256([]byte(run.UID))
		name = strings.TrimRight(name[:63-len(suffix)-10], ".-") + "-" + hex.EncodeToString(sum[:4])
	}
	return name + "-" + suffix
}

func runConstantKey(ref *corev1.SecretKeySelector) string {
	sum := sha256.Sum256([]byte(ref.Name + "/" + ref.Key))
	return "constant-" + hex.EncodeToString(sum[:12])
}

func (r *AnonymizationRunReconciler) prepareRunSnapshot(ctx context.Context, run *api.AnonymizationRun) (*runSnapshot, error) {
	if r.APIReader == nil {
		return nil, errors.New("run snapshot requires an uncached APIReader")
	}
	seed := &corev1.Secret{}
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: runChildName(run, "seed")}, seed)
	if err == nil {
		snapshot, err := decodeRunSnapshot(run, seed)
		if err != nil {
			return nil, err
		}
		return snapshot, r.ensureRunPolicyConfigMap(ctx, run, seed)
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	policy := &api.AnonymizationPolicy{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.PolicyRef.Name}, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, invalidRun(runConditionPolicy, api.ReasonPolicyNotFound, "the referenced Policy does not exist")
		}
		return nil, err
	}
	valid := meta.FindStatusCondition(policy.Status.Conditions, conditionPolicyValid)
	canonical, hash, err := runnerpolicy.Snapshot(policy.Spec)
	if err != nil || valid == nil || valid.Status != metav1.ConditionTrue || valid.ObservedGeneration != policy.Generation || policy.Status.Hash != hash {
		return nil, invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "the Policy must be valid at its current generation")
	}
	frozen, copied, err := r.validateRunInputs(ctx, run)
	if err != nil {
		return nil, err
	}
	data := map[string][]byte{runPolicyFile: canonical, runPolicyHashKey: []byte(hash)}
	for key, value := range copied.Data {
		data["system-"+key] = bytes.Clone(value)
	}
	if err := r.snapshotRunPolicyPayloads(ctx, run.Namespace, policy.Spec, data); err != nil {
		return nil, err
	}
	fetchContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source, reason, endpoint, region, err := r.resolveRunSource(fetchContext, run)
	if err != nil {
		return nil, err
	}
	if frozen.Spec.Source.Restore.EndpointURL == "" {
		frozen.Spec.Source.Restore.EndpointURL = endpoint
	}
	if frozen.Spec.Source.Restore.Region == "" {
		frozen.Spec.Source.Restore.Region = region
	}
	location, _ := url.Parse(source.Destination)
	restoreStorage, err := objectstore.Resolve(ctx, r.APIReader, run.Namespace, api.ObjectStorageSpec{
		Bucket: location.Host, EndpointURL: frozen.Spec.Source.Restore.EndpointURL, Region: frozen.Spec.Source.Restore.Region,
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: frozen.Spec.Source.Restore.CredentialsSecret},
	})
	if err != nil {
		return nil, invalidRun(runConditionSource, api.ReasonInvalidDestination, "restore credentials or endpoint are invalid")
	}
	frozen.Spec.Source.Restore.EndpointURL = restoreStorage.EndpointURL()
	frozen.Spec.Source.Restore.Region = restoreStorage.Region()
	data[runSpecFile], err = json.Marshal(frozen.Spec)
	if err != nil {
		return nil, err
	}
	data[runSourceFile], err = json.Marshal(runSourceSnapshot{Source: source, Reason: reason, OutputGroup: runOutputGroup(run)})
	if err != nil {
		return nil, err
	}
	immutable := true
	seed = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: runChildName(run, "seed"), Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{runOwner(run)}}, Immutable: &immutable, Type: corev1.SecretTypeOpaque, Data: data}
	// One immutable Secret commits the whole snapshot before its public ConfigMap is derived.
	if err := r.createRunChild(ctx, run, seed); err != nil {
		return nil, err
	}
	persisted, err := r.loadRunSnapshot(ctx, run)
	if err != nil {
		return nil, err
	}
	return persisted, r.ensureRunPolicyConfigMap(ctx, run, persisted.Payload)
}

func (r *AnonymizationRunReconciler) snapshotRunPolicyPayloads(ctx context.Context, namespace string, policy api.AnonymizationPolicySpec, data map[string][]byte) error {
	for _, step := range policy.Steps {
		var value []byte
		if step.ConfigMapKeyRef != nil {
			ref := step.ConfigMapKeyRef
			object := &corev1.ConfigMap{}
			if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, object); err != nil {
				return invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "a SQL ConfigMap is unavailable")
			}
			text, ok := object.Data[ref.Key]
			if ok {
				value = []byte(text)
			} else {
				value, ok = object.BinaryData[ref.Key]
				if !ok {
					return invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "a SQL ConfigMap key is missing")
				}
			}
		} else {
			var err error
			value, err = r.runSecretValue(ctx, namespace, step.SecretKeyRef)
			if err != nil {
				return err
			}
		}
		data["sql-"+step.Name] = bytes.Clone(value)
	}
	for _, database := range policy.Databases {
		for _, table := range database.Tables {
			for _, column := range table.Columns {
				if column.Params != nil && column.Params.ValueFrom != nil {
					value, err := r.runSecretValue(ctx, namespace, column.Params.ValueFrom)
					if err != nil {
						return err
					}
					data[runConstantKey(column.Params.ValueFrom)] = value
				}
			}
		}
	}
	if policy.Determinism.Mode == runFixedSeedMode {
		value, err := r.runSecretValue(ctx, namespace, policy.Determinism.SeedSecretRef)
		if err != nil {
			return err
		}
		if len(value) < 32 {
			return invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "the fixed seed must contain at least 32 bytes")
		}
		data[runSeedKey] = value
	} else {
		data[runSeedKey] = make([]byte, 32)
		if _, err := rand.Read(data[runSeedKey]); err != nil {
			return err
		}
	}
	return nil
}

func (r *AnonymizationRunReconciler) runSecretValue(ctx context.Context, namespace string, ref *corev1.SecretKeySelector) ([]byte, error) {
	if ref == nil {
		return nil, invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "a required Secret key selector is missing")
	}
	secret := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		return nil, invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "a referenced policy Secret is unavailable")
	}
	value, ok := secret.Data[ref.Key]
	if !ok {
		return nil, invalidRun(runConditionPolicy, api.ReasonPolicyInvalid, "a referenced policy Secret key is missing")
	}
	return bytes.Clone(value), nil
}

func (r *AnonymizationRunReconciler) loadRunSnapshot(ctx context.Context, run *api.AnonymizationRun) (*runSnapshot, error) {
	if r.APIReader == nil {
		return nil, errors.New("run snapshot requires APIReader")
	}
	secret := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: runChildName(run, "seed")}, secret); err != nil {
		return nil, err
	}
	return decodeRunSnapshot(run, secret)
}

func decodeRunSnapshot(run *api.AnonymizationRun, seed *corev1.Secret) (*runSnapshot, error) {
	if err := runOwns(run, seed); err != nil {
		return nil, err
	}
	if !seed.DeletionTimestamp.IsZero() || seed.Immutable == nil || !*seed.Immutable || len(seed.Data[runSeedKey]) == 0 {
		return nil, errors.New("run snapshot must be immutable and complete")
	}
	snapshot := &runSnapshot{Payload: seed, PolicyHash: string(seed.Data[runPolicyHashKey])}
	if err := json.Unmarshal(seed.Data[runSpecFile], &snapshot.Spec); err != nil {
		return nil, err
	}
	policy, hash, err := runnerpolicy.Decode(seed.Data[runPolicyFile])
	if err != nil || hash != snapshot.PolicyHash {
		return nil, errors.New("run policy snapshot hash is invalid")
	}
	snapshot.Policy = policy
	var source runSourceSnapshot
	if err := json.Unmarshal(seed.Data[runSourceFile], &source); err != nil {
		return nil, err
	}
	snapshot.Source, snapshot.SourceReason = source.Source, source.Reason
	snapshot.OutputGroup = source.OutputGroup
	if snapshot.Spec.TempCluster.SystemUsersSecretRef == nil {
		return nil, errors.New("snapshot source credentials reference is missing")
	}
	snapshot.SystemUsers = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: snapshot.Spec.TempCluster.SystemUsersSecretRef.Name, Namespace: run.Namespace}, Data: map[string][]byte{}}
	for _, key := range runSystemKeys {
		if value, ok := seed.Data["system-"+key]; ok {
			snapshot.SystemUsers.Data[key] = bytes.Clone(value)
		}
	}
	return snapshot, nil
}

func (r *AnonymizationRunReconciler) ensureRunPolicyConfigMap(ctx context.Context, run *api.AnonymizationRun, seed *corev1.Secret) error {
	immutable := true
	data := map[string]string{}
	for _, key := range []string{runPolicyFile, runSpecFile, runSourceFile, runPolicyHashKey} {
		data[key] = string(seed.Data[key])
	}
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: runChildName(run, runPolicyVolume), Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{runOwner(run)}}, Immutable: &immutable, Data: data}
	if err := r.createRunChild(ctx, run, configMap); err != nil {
		return err
	}
	current := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(configMap), current); err != nil {
		return err
	}
	if current.Immutable == nil || !*current.Immutable || len(current.Data) != len(data) {
		return errors.New("policy ConfigMap snapshot was changed")
	}
	for key, value := range data {
		if current.Data[key] != value {
			return errors.New("policy ConfigMap differs from immutable snapshot")
		}
	}
	return nil
}

func (r *AnonymizationRunReconciler) resolveRunSource(ctx context.Context, run *api.AnonymizationRun) (api.ResolvedSource, string, string, string, error) {
	var source api.ResolvedSource
	reason, endpoint, region := api.ReasonLiteral, "", ""
	input := run.Spec.Source
	switch {
	case input.BackupPointerRef != nil:
		bp := &api.BackupPointer{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: input.BackupPointerRef.Name}, bp); err != nil {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonPointerNotReady, "the referenced BackupPointer is unavailable")
		}
		ready := meta.FindStatusCondition(bp.Status.Conditions, conditionReady)
		if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != bp.Generation || bp.Status.Current == nil {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonPointerNotReady, "the BackupPointer is not ready at its current generation")
		}
		source = api.ResolvedSource{BackupName: bp.Status.Current.BackupName, Destination: bp.Status.Current.Destination,
			SourceCluster: bp.Spec.Source.PXCCluster, PointerPublishedAt: bp.Status.Current.PublishedAt, PointerSchemaVersion: bp.Status.Current.SchemaVersion}
		reason = api.ReasonFromBackupPointer
		if meta.IsStatusConditionFalse(bp.Status.Conditions, conditionFresh) && r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, api.ReasonStaleBackup, "ResolveSource", "the selected BackupPointer is stale")
		}
	case input.BackupRef != nil:
		backup := &unstructured.Unstructured{}
		backup.SetGroupVersionKind(pxc.BackupGVK)
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: input.BackupRef.Name}, backup); err != nil {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonBackupNotSucceeded, "the referenced backup is unavailable")
		}
		view, err := pxc.DecodeBackup(backup)
		if err != nil || !view.Succeeded() {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonBackupNotSucceeded, "the referenced backup must be Succeeded")
		}
		source = api.ResolvedSource{BackupName: view.Name, Destination: view.Status.Destination, SourceCluster: view.Spec.PXCCluster}
		if view.Status.S3 != nil {
			endpoint, region = view.Status.S3.EndpointURL, view.Status.S3.Region
		}
		reason = api.ReasonFromBackup
	case input.Pointer != nil:
		var document *pointer.Document
		var err error
		if r.fetchPointer != nil {
			document, err = r.fetchPointer(ctx, run.Namespace, *input.Pointer)
		} else {
			document, err = (pointer.Fetcher{Reader: r.APIReader, Namespace: run.Namespace}).Fetch(ctx, *input.Pointer)
		}
		if err != nil || document == nil {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonPointerFetchFailed, "the source pointer could not be fetched")
		}
		if err := document.Validate(); err != nil {
			return source, "", "", "", invalidRun(runConditionSource, api.ReasonInvalidDestination, "the source pointer is invalid")
		}
		source = api.ResolvedSource{BackupName: document.Name, Destination: document.Destination, PointerSchemaVersion: int32(document.SchemaVersion)}
		if document.PublishedAt != nil {
			published := metav1.NewTime(*document.PublishedAt)
			source.PointerPublishedAt = &published
		}
		if document.SourceCluster != nil {
			source.SourceCluster = document.SourceCluster.Name
		}
		if document.S3 != nil {
			endpoint, region = document.S3.EndpointURL, document.S3.Region
		}
		reason = api.ReasonFromPointer
	default:
		source.Destination = input.Destination
		parts := strings.Split(strings.TrimSuffix(source.Destination, "/"), "/")
		source.BackupName = parts[len(parts)-1]
	}
	if err := (&pointer.Document{Name: source.BackupName, Destination: source.Destination, SchemaVersion: pointer.SchemaVersion}).Validate(); err != nil {
		return source, "", "", "", invalidRun(runConditionSource, api.ReasonInvalidDestination, "the source must identify a valid S3 backup prefix")
	}
	return source, reason, endpoint, region, nil
}

func (r *AnonymizationRunReconciler) cleanupRun(ctx context.Context, run *api.AnonymizationRun, deleting bool) (ctrl.Result, error) {
	if (deleting || meta.IsStatusConditionTrue(run.Status.Conditions, runConditionFailed)) && run.Status.Anonymize != nil && run.Status.Anonymize.JobName != "" {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: run.Status.Anonymize.JobName, Namespace: run.Namespace}}
		gone, err := r.deleteRunOwned(ctx, run, job)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !gone {
			return ctrl.Result{RequeueAfter: runPollInterval}, nil
		}
	}
	if !deleting && run.Spec.Cleanup.OnFailure == "Retain" && meta.IsStatusConditionTrue(run.Status.Conditions, runConditionFailed) {
		base := run.DeepCopy()
		r.setRunCondition(run, runConditionRetained, metav1.ConditionTrue, api.ReasonRetained, "the temporary cluster is retained until Run deletion")
		return r.patchRun(ctx, run, base, 0)
	}
	if result, handled, err := r.cleanupRunCluster(ctx, run); handled || err != nil {
		return result, err
	}
	base := run.DeepCopy()
	if run.Status.TempCluster != nil && run.Status.TempCluster.DeletedAt == nil {
		now := metav1.NewTime(r.runTime())
		run.Status.TempCluster.DeletedAt = &now
	}
	r.setRunCondition(run, runConditionCleaned, metav1.ConditionTrue, api.ReasonTempClusterDeleted, "the temporary cluster and owned credentials are absent")
	if !deleting && !meta.IsStatusConditionTrue(run.Status.Conditions, runConditionFailed) {
		run.Status.Phase = api.RunPhaseCompleted
		now := metav1.NewTime(r.runTime())
		run.Status.CompletedAt = &now
		r.setRunCondition(run, runConditionComplete, metav1.ConditionTrue, api.ReasonSucceeded, "the pipeline and temporary resource cleanup finished")
	}
	if deleting {
		if !apiequality.Semantic.DeepEqual(run.Status, base.Status) {
			return r.patchRun(ctx, run, base, time.Nanosecond)
		}
		controllerutil.RemoveFinalizer(run, runFinalizer)
		err := r.Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		return ctrl.Result{}, err
	}
	return r.patchRun(ctx, run, base, 0)
}

func (r *AnonymizationRunReconciler) deleteRunOwned(ctx context.Context, run *api.AnonymizationRun, object client.Object) (bool, error) {
	reader := client.Reader(r.Client)
	if _, ok := object.(*corev1.Secret); ok {
		reader = r.APIReader
	}
	if reader == nil {
		return false, errors.New("uncached APIReader is required for cleanup")
	}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if err := runOwns(run, object); err != nil {
		return false, err
	}
	if object.GetDeletionTimestamp().IsZero() {
		if err := r.deleteRunObject(ctx, object); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *AnonymizationRunReconciler) deleteRunObject(ctx context.Context, object client.Object) error {
	uid, version := object.GetUID(), object.GetResourceVersion()
	if uid == "" {
		return errors.New("owned child must have a persisted UID before deletion")
	}
	return client.IgnoreNotFound(r.Delete(ctx, object, client.PropagationPolicy(metav1.DeletePropagationForeground),
		client.Preconditions{UID: &uid, ResourceVersion: &version}))
}

func (r *AnonymizationRunReconciler) deleteRunGeneratedSecrets(ctx context.Context, run *api.AnonymizationRun) (bool, error) {
	name, uid := run.Status.TempCluster.Name, run.Status.TempCluster.UID
	if uid == "" {
		return true, nil
	}
	absent := true
	for _, name := range []string{"internal-" + name, name + "-ssl", name + "-ssl-internal", name + "-ca-cert"} {
		secret := &corev1.Secret{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		owner := metav1.GetControllerOf(secret)
		if owner == nil || owner.UID != uid || owner.Kind != pxc.ClusterGVK.Kind || owner.Name != run.Status.TempCluster.Name {
			continue
		}
		absent = false
		if err := r.deleteRunObject(ctx, secret); err != nil {
			return false, err
		}
	}
	return absent, nil
}

func (r *AnonymizationRunReconciler) renderRunJob(run *api.AnonymizationRun, snapshot *runSnapshot) (*batchv1.Job, error) {
	image := run.Spec.Runner.Image
	if image == "" {
		image = r.RunnerImage
	}
	if image == "" {
		return nil, errors.New("runner image is required")
	}
	if run.Status.Anonymize == nil || run.Status.TempCluster == nil || run.CreationTimestamp.IsZero() {
		return nil, errors.New("durable runner identity and reference time are required")
	}
	workers, pageSize := run.Spec.Runner.Workers, int32(5000)
	if workers == 0 {
		workers = 4
	}
	if run.Spec.Runner.PageSize != nil {
		pageSize = *run.Spec.Runner.PageSize
	}
	if workers < 1 || workers > 32 || pageSize < 1 {
		return nil, errors.New("runner workers or page size is invalid")
	}
	disableBinlog := run.Spec.Runner.DisableBinlog == nil || *run.Spec.Runner.DisableBinlog
	root := "/etc/pxc-anonymizer"
	args := []string{runContainerName, "--policy=" + root + "/policy/policy.json", "--host=" + run.Status.TempCluster.Name + "-pxc." + run.Namespace + ".svc",
		"--port=3306", "--user=root", "--password-file=" + root + "/creds/root", "--seed-file=" + root + "/seed/seed",
		"--run-uid=" + string(run.UID), "--attempt=" + strconv.Itoa(int(run.Status.Anonymize.Attempts)),
		"--workers=" + strconv.Itoa(int(workers)), "--page-size=" + strconv.Itoa(int(pageSize)), "--disable-binlog=" + strconv.FormatBool(disableBinlog),
		"--progress-interval=10s", "--log-format=json", "--reference-time=" + run.CreationTimestamp.UTC().Format(time.RFC3339),
		"--steps-dir=" + root + "/steps", "--constants-dir=" + root + "/constants", "--tls-mode=disabled", "--termination-file=/dev/termination-log"}
	if run.Spec.Runner.DryRun {
		args = append(args, "--dry-run")
	}
	items := []corev1.KeyToPath{{Key: runSeedKey, Path: "seed/seed"}, {Key: "system-root", Path: "creds/root"}}
	for _, step := range snapshot.Policy.Steps {
		items = append(items, corev1.KeyToPath{Key: "sql-" + step.Name, Path: "steps/" + step.Name + ".sql"})
	}
	seen := map[string]bool{}
	for _, database := range snapshot.Policy.Databases {
		for _, table := range database.Tables {
			for _, column := range table.Columns {
				if column.Params == nil || column.Params.ValueFrom == nil {
					continue
				}
				ref := column.Params.ValueFrom
				key := runConstantKey(ref)
				if !seen[key] {
					items = append(items, corev1.KeyToPath{Key: key, Path: "constants/" + ref.Name + "/" + ref.Key})
					seen[key] = true
				}
			}
		}
	}
	slices.SortFunc(items, func(a, b corev1.KeyToPath) int { return strings.Compare(a.Path, b.Path) })
	no, yes, uid, mode, zero := false, true, int64(65532), int32(0400), int32(0)
	deadline := run.Spec.Timeouts.Anonymize.Duration
	if deadline <= 0 {
		deadline = 6 * time.Hour
	}
	seconds := max(int64(1), int64(deadline/time.Second))
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: run.Status.Anonymize.JobName, Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{runOwner(run)}}, Spec: batchv1.JobSpec{
		BackoffLimit: &zero, ActiveDeadlineSeconds: &seconds, TTLSecondsAfterFinished: run.Spec.TTLSecondsAfterFinished,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{pxc.LabelRun: pxc.LabelValue(run.Name)}}, Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			NodeSelector: run.Spec.Runner.NodeSelector, Tolerations: run.Spec.Runner.Tolerations, Affinity: run.Spec.Runner.Affinity,
			Containers: []corev1.Container{{Name: runContainerName, Image: image, Command: []string{"/manager"}, Args: args,
				Resources: run.Spec.Runner.Resources, TerminationMessagePath: "/dev/termination-log", TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{{Name: runPolicyVolume, MountPath: root + "/policy", ReadOnly: true},
					{Name: "payload", MountPath: root, ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}}}},
			Volumes: []corev1.Volume{
				{Name: runPolicyVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: runChildName(run, runPolicyVolume)},
					Items: []corev1.KeyToPath{{Key: runPolicyFile, Path: runPolicyFile}}, DefaultMode: &mode}}},
				{Name: "payload", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: runChildName(run, "seed"), Items: items, DefaultMode: &mode}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		}},
	}}
	return job, nil
}

func runOutputGroup(run *api.AnonymizationRun) string {
	if group := run.Labels[pxc.LabelOutputGroup]; group != "" {
		return pxc.LabelValue(group)
	}
	return pxc.LabelValue(run.Name)
}

func (r *AnonymizationRunReconciler) runsForOutputBackup(ctx context.Context, object client.Object) []ctrl.Request {
	label := object.GetLabels()[pxc.LabelRun]
	if label == "" {
		return nil
	}
	runs := &api.AnonymizationRunList{}
	if err := r.List(ctx, runs, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]ctrl.Request, 0, 1)
	for _, run := range runs.Items {
		if pxc.LabelValue(run.Name) == label {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&run)})
		}
	}
	return requests
}

func runReport(terminated *corev1.ContainerStateTerminated, hash string) (report.Report, bool) {
	var result report.Report
	if terminated == nil || len(terminated.Message) > 4096 || json.Unmarshal([]byte(terminated.Message), &result) != nil {
		return result, false
	}
	if result.Result != report.ResultOK && result.Result != report.ResultError {
		return result, false
	}
	if (result.Result == report.ResultOK && result.Class != "") || result.TablesDone < 0 || result.TablesTotal < result.TablesDone ||
		result.TablesTotal > 2147483647 || result.RowsDone < 0 || result.StepsDone < 0 || result.StepsDone > 2147483647 {
		return result, false
	}
	if result.PolicyHash != hash || result.ExitCode() != int(terminated.ExitCode) {
		return result, false
	}
	if result.Result == report.ResultError && result.Class != report.Transient && result.Class != report.Policy &&
		result.Class != report.Schema && result.Class != report.Permission && result.Class != report.Unique {
		return result, false
	}
	return result, true
}

func runReportReason(class report.Class) string {
	switch class {
	case report.Policy:
		return api.ReasonPolicyError
	case report.Schema:
		return api.ReasonSchemaMismatch
	case report.Permission:
		return api.ReasonPermissionDenied
	case report.Unique:
		return api.ReasonUniqueViolation
	default:
		return api.ReasonRunnerFailed
	}
}

func (r *AnonymizationRunReconciler) validateRunInputs(ctx context.Context, run *api.AnonymizationRun) (*api.AnonymizationRun, *corev1.Secret, error) {
	frozen := run.DeepCopy()
	if frozen.Spec.Runner.Image == "" {
		frozen.Spec.Runner.Image = r.RunnerImage
	}
	if frozen.Spec.Runner.Image == "" || frozen.Spec.Runner.Workers < 0 || frozen.Spec.Runner.Workers > 32 ||
		(frozen.Spec.Runner.PageSize != nil && *frozen.Spec.Runner.PageSize < 1) || frozen.Spec.BackoffLimit < 0 {
		return nil, nil, invalidRun(runConditionAnonymized, api.ReasonPolicyError, "runner image and execution settings must be valid")
	}
	if frozen.Spec.TempCluster.SystemUsersSecretRef == nil || frozen.Spec.TempCluster.SystemUsersSecretRef.Name == "" {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "tempCluster.systemUsersSecretRef is required for restore")
	}
	users := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: frozen.Spec.TempCluster.SystemUsersSecretRef.Name}, users); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "the source system-users Secret does not exist")
		}
		return nil, nil, err
	}
	copied, err := pxc.RenderSystemUsersSecret(frozen, runTempName(run), users)
	if err != nil {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "source system-users Secret requires root, xtrabackup, monitor, proxyadmin, operator and replication keys")
	}
	if keys := frozen.Spec.Output.ObjectStorage.Keys; keys != nil &&
		((keys.AccessKeyID != "" && keys.AccessKeyID != runAWSAccessKey) ||
			(keys.SecretAccessKey != "" && keys.SecretAccessKey != runAWSSecretKey)) {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "Percona backups require standard AWS credential keys")
	}
	if frozen.Spec.Output.ObjectStorage.CredentialsSecretRef == nil {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "output storage requires a credentials Secret")
	}
	storage, err := objectstore.Resolve(ctx, r.APIReader, run.Namespace, frozen.Spec.Output.ObjectStorage)
	if err != nil {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "output object-storage credentials or endpoint are invalid")
	}
	frozen.Spec.Output.ObjectStorage.EndpointURL = storage.EndpointURL()
	frozen.Spec.Output.ObjectStorage.Region = storage.Region()
	if _, err := pxc.RenderTempCluster(frozen, runTempName(run)); err != nil {
		return nil, nil, invalidRun(runConditionCluster, api.ReasonClusterError, "temporary cluster configuration is invalid")
	}
	return frozen, copied, nil
}

func (r *AnonymizationRunReconciler) cleanupRunCluster(ctx context.Context, run *api.AnonymizationRun) (ctrl.Result, bool, error) {
	if run.Status.TempCluster != nil && run.Status.TempCluster.Name != "" {
		cluster := &unstructured.Unstructured{}
		cluster.SetGroupVersionKind(pxc.ClusterGVK)
		cluster.SetName(run.Status.TempCluster.Name)
		cluster.SetNamespace(run.Namespace)
		err := r.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
		if err == nil {
			if err := runOwns(run, cluster); err != nil {
				return ctrl.Result{}, true, err
			}
			if run.Status.TempCluster.UID == "" {
				base := run.DeepCopy()
				run.Status.TempCluster.UID = cluster.GetUID()
				result, err := r.patchRun(ctx, run, base, time.Nanosecond)
				return result, true, err
			}
			if cluster.GetUID() != run.Status.TempCluster.UID {
				return ctrl.Result{}, true, errors.New("temporary cluster UID changed")
			}
			if cluster.GetDeletionTimestamp().IsZero() {
				if err := r.deleteRunObject(ctx, cluster); err != nil {
					return ctrl.Result{}, true, err
				}
			}
			base := run.DeepCopy()
			message := "waiting for temporary cluster and volume deletion"
			if condition := meta.FindStatusCondition(run.Status.Conditions, runConditionCleaned); condition != nil &&
				r.runExpired(&condition.LastTransitionTime, run.Spec.Timeouts.Cleanup.Duration, 30*time.Minute) {
				message = "cleanup deadline exceeded; owned cluster deletion must still finish"
			}
			r.setRunCondition(run, runConditionCleaned, metav1.ConditionFalse, api.ReasonDeleting, message)
			result, err := r.patchRun(ctx, run, base, runPollInterval)
			return result, true, err
		}
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, true, err
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: run.Status.TempCluster.SecretName, Namespace: run.Namespace}}
		if secret.Name != "" {
			gone, err := r.deleteRunOwned(ctx, run, secret)
			if err != nil {
				return ctrl.Result{}, true, err
			}
			if !gone {
				return ctrl.Result{RequeueAfter: runPollInterval}, true, nil
			}
		}
		gone, err := r.deleteRunGeneratedSecrets(ctx, run)
		if err != nil {
			return ctrl.Result{}, true, err
		}
		if !gone {
			return ctrl.Result{RequeueAfter: runPollInterval}, true, nil
		}
	}
	return ctrl.Result{}, false, nil
}
