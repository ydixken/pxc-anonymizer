// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/pxc"
)

const (
	runJobRoot            = "/etc/pxc-anonymizer"
	runJobUIDField        = "uid"
	runJobPolicyVolume    = "policy"
	runJobPrivateConstant = "synthetic-constant"
)

func runJobFixture(t *testing.T, mutate func(*api.AnonymizationRun, []client.Object)) (*runTestState, *runSnapshot, *batchv1.Job) {
	t.Helper()
	state := newRunTest(t, mutate)
	state.toProvisioning(t)
	snapshot, err := state.reconciler.loadRunSnapshot(t.Context(), state.run)
	if err != nil {
		t.Fatal(err)
	}
	state.run.Status.Anonymize = &api.AnonymizeStatus{JobName: runChildName(state.run, "anonymize-1"), Attempts: 1}
	return state, snapshot, renderRunJobForTest(t, state, snapshot)
}

func renderRunJobForTest(t *testing.T, state *runTestState, snapshot *runSnapshot) *batchv1.Job {
	t.Helper()
	job, err := state.reconciler.renderRunJob(state.run, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestRunJobPodSecurity(t *testing.T) {
	state, _, job := runJobFixture(t, nil)
	pod := job.Spec.Template.Spec
	security := pod.SecurityContext
	if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot {
		t.Fatal("Job must require a nonroot process")
	}
	for name, value := range map[string]*int64{runJobUIDField: security.RunAsUser, "gid": security.RunAsGroup, "fsGroup": security.FSGroup} {
		if value == nil || *value != 65532 {
			t.Fatalf("%s must be 65532 for projected-file access", name)
		}
	}
	if security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("Job must use RuntimeDefault seccomp")
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || len(pod.InitContainers) != 0 {
		t.Fatal("Job must not mount an API token or depend on an init container")
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("Job must isolate host namespaces and leave retries to the controller")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatal("Kubernetes must not retry the anonymizer independently")
	}
	if job.Namespace != state.run.Namespace || len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != state.run.UID {
		t.Fatal("Job must belong to this namespaced Run UID")
	}
}

func TestRunJobContainerSecurity(t *testing.T) {
	_, _, job := runJobFixture(t, nil)
	pod := job.Spec.Template.Spec
	if len(pod.Containers) != 1 || len(pod.EphemeralContainers) != 0 {
		t.Fatal("Job must contain exactly one anonymizer")
	}
	container := pod.Containers[0]
	security := container.SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		t.Fatal("container must prohibit privilege escalation and root-filesystem writes")
	}
	if security.Capabilities == nil || !reflect.DeepEqual(security.Capabilities.Drop, []corev1.Capability{"ALL"}) ||
		len(security.Capabilities.Add) != 0 || (security.Privileged != nil && *security.Privileged) {
		t.Fatal("container must drop every capability without privileged access")
	}
	if len(container.Env) != 0 || len(container.EnvFrom) != 0 || !reflect.DeepEqual(container.Command, []string{"/manager"}) {
		t.Fatal("runner must receive file references directly, without environment payloads or a shell")
	}
	expected := []corev1.VolumeMount{
		{Name: runJobPolicyVolume, MountPath: runJobRoot + "/policy", ReadOnly: true},
		{Name: "payload", MountPath: runJobRoot, ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}
	if !reflect.DeepEqual(container.VolumeMounts, expected) {
		t.Fatalf("unexpected writable or additional mounts: %#v", container.VolumeMounts)
	}
}

func TestRunJobPayloadProjection(t *testing.T) {
	state, snapshot, job := runJobFixture(t, nil)
	volumes := job.Spec.Template.Spec.Volumes
	if len(volumes) != 3 || volumes[0].ConfigMap == nil || volumes[1].Secret == nil || volumes[2].EmptyDir == nil {
		t.Fatal("Job needs only the policy ConfigMap, private payload Secret and temporary directory")
	}
	policy, payload := volumes[0].ConfigMap, volumes[1].Secret
	for name, mode := range map[string]*int32{runJobPolicyVolume: policy.DefaultMode, "payload": payload.DefaultMode} {
		if mode == nil || *mode != 0400 {
			t.Fatalf("%s must declare mode 0400; fsGroup supplies effective group-read access", name)
		}
	}
	if policy.Name != runChildName(state.run, runJobPolicyVolume) || payload.SecretName != snapshot.Payload.Name ||
		!reflect.DeepEqual(policy.Items, []corev1.KeyToPath{{Key: runPolicyFile, Path: runPolicyFile}}) {
		t.Fatal("Job must project only the frozen policy file and its own immutable payload")
	}
	expected := map[string]string{
		"seed/seed":                       "synthetic-fixed-seed-with-32-bytes-minimum",
		"creds/root":                      "synthetic-root",
		"steps/setup.sql":                 "SELECT 1;",
		"steps/private.sql":               "SELECT 2;",
		"constants/policy-constant/value": runJobPrivateConstant,
	}
	if len(payload.Items) != len(expected) {
		t.Fatalf("projected paths=%d, want %d", len(payload.Items), len(expected))
	}
	jobJSON, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range payload.Items {
		value, exists := expected[item.Path]
		if !exists || item.Mode != nil || string(snapshot.Payload.Data[item.Key]) != value {
			t.Fatalf("unexpected projection or unfrozen payload at %q", item.Path)
		}
		delete(expected, item.Path)
		if bytes.Contains(jobJSON, []byte(value)) || bytes.Contains(jobJSON, []byte(base64.StdEncoding.EncodeToString([]byte(value)))) {
			t.Fatalf("private payload for %q leaked into the Job object", item.Path)
		}
	}
	if len(expected) != 0 {
		t.Fatal("a required seed, password, SQL step or constant projection is missing")
	}
}

func TestRunJobFrozenInputs(t *testing.T) {
	state, snapshot, first := runJobFixture(t, nil)
	state.clock = state.clock.Add(48 * time.Hour)
	state.run.Status.Anonymize.Attempts = 2
	state.run.Status.Anonymize.JobName = runChildName(state.run, "anonymize-2")
	second := renderRunJobForTest(t, state, snapshot)
	expected := []string{
		"--policy=" + runJobRoot + "/policy/policy.json",
		"--password-file=" + runJobRoot + "/creds/root",
		"--seed-file=" + runJobRoot + "/seed/seed",
		"--steps-dir=" + runJobRoot + "/steps",
		"--constants-dir=" + runJobRoot + "/constants",
		"--reference-time=2026-01-02T03:04:05Z",
		"--run-uid=" + string(state.run.UID),
	}
	for _, job := range []*batchv1.Job{first, second} {
		for _, argument := range expected {
			if !slices.Contains(job.Spec.Template.Spec.Containers[0].Args, argument) {
				t.Fatalf("Job %s lacks stable execution argument %q", job.Name, argument)
			}
		}
	}
	if !slices.Contains(first.Spec.Template.Spec.Containers[0].Args, "--attempt=1") ||
		!slices.Contains(second.Spec.Template.Spec.Containers[0].Args, "--attempt=2") {
		t.Fatal("retry must advance its attempt while retaining frozen inputs")
	}
	if !reflect.DeepEqual(first.Spec.Template.Spec.Volumes, second.Spec.Template.Spec.Volumes) {
		t.Fatal("retry must reuse the same policy and private payload projections")
	}
}

func TestRunJobImageAndLongNameLabels(t *testing.T) {
	state, snapshot, job := runJobFixture(t, func(run *api.AnonymizationRun, _ []client.Object) {
		run.Name = strings.Repeat("r", 64)
	})
	if job.Spec.Template.Spec.Containers[0].Image != state.reconciler.RunnerImage {
		t.Fatal("unspecified Run image must use the configured runner image")
	}
	labels := job.Spec.Template.Labels
	if len(labels) == 0 || labels[pxc.LabelRun] != pxc.LabelValue(state.run.Name) {
		t.Fatal("long Run identity must use the shared label representation")
	}
	for key, value := range labels {
		if problems := validation.IsValidLabelValue(value); len(problems) != 0 {
			t.Fatalf("invalid label %s: %v", key, problems)
		}
	}
	if job.OwnerReferences[0].Name != state.run.Name || job.Name != state.run.Status.Anonymize.JobName {
		t.Fatal("label shortening must preserve full owner and recorded child identities")
	}
	state.run.Spec.Runner.Image = "registry.example/custom-runner:pinned"
	if got := renderRunJobForTest(t, state, snapshot).Spec.Template.Spec.Containers[0].Image; got != state.run.Spec.Runner.Image {
		t.Fatalf("explicit runner image lost precedence: %s", got)
	}
	state.run.Spec.Runner.Image, state.reconciler.RunnerImage = "", ""
	if _, err := state.reconciler.renderRunJob(state.run, snapshot); err == nil {
		t.Fatal("absent explicit and configured images must fail")
	}
}
