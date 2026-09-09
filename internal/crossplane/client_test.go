package crossplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	testGroup          = "mysql.sql.crossplane.io"
	testVersion        = "v1alpha1"
	testOwner          = "bootstrap-uid"
	testName           = "example-user"
	testUsers          = "users"
	testGrants         = "grants"
	testOriginalUID    = "original"
	testReplacementUID = "replacement"
	testMissing        = "missing"
	testHuman          = "human"
	testLabel          = "project"
	testFinalizer      = "example.com/retain"
	testConditionType  = "type"
)

func TestSelectionUsesClusterScopeAndExactSelector(t *testing.T) {
	a := managed(testUsers, "a", "uid-a")
	b := managed(testUsers, "b", "uid-b")
	other := managed(testUsers, "other", "uid-other")
	other.SetLabels(map[string]string{testLabel: "other"})
	g := managed(testGrants, "grant", "uid-grant")
	helper, client := testClient(t, b, other, g, a)
	refs, err := helper.Select(t.Context(), []string{testUsers, testGrants}, &metav1.LabelSelector{
		MatchLabels: map[string]string{testLabel: "example"},
	})
	if err != nil || len(refs) != 3 || refs[0].Name != "a" || refs[1].Name != "b" || refs[2].Kind != testGrants {
		t.Fatalf("unexpected selected identities: %v, %v", refs, err)
	}
	for _, action := range client.Actions() {
		if action.GetNamespace() != "" || action.GetResource().Group != testGroup || action.GetResource().Version != testVersion {
			t.Fatalf("incorrect API scope: %#v", action)
		}
		if action.(clienttesting.ListAction).GetListRestrictions().Labels.String() != "project=example" {
			t.Fatal("label selector was not sent to the API")
		}
	}
}

func TestPauseResumeRecoversOwnershipAndPreservesHumanPause(t *testing.T) {
	ours := managed(testUsers, testName, "uid-ours")
	ours.SetAnnotations(map[string]string{"example.com/keep": "yes"})
	human := managed(testUsers, testHuman, "uid-human")
	human.SetAnnotations(map[string]string{PausedAnnotation: annotationTrue})
	helper, client := testClient(t, ours, human)
	refs := []Reference{reference(testUsers, ours), reference(testUsers, human)}
	paused, err := helper.Pause(t.Context(), testOwner, refs)
	if err != nil || len(paused) != 1 || paused[0].Name != testName {
		t.Fatalf("pause = %v, %v", paused, err)
	}
	// Retrying the original selection recovers ownership after an unsuccessful Bootstrap status write.
	recovered, err := helper.Pause(t.Context(), testOwner, refs)
	if err != nil || len(recovered) != 1 || recovered[0] != paused[0] {
		t.Fatalf("recovery = %v, %v", recovered, err)
	}
	resumed, err := helper.Resume(t.Context(), testOwner, recovered)
	if err != nil || len(resumed) != 1 {
		t.Fatalf("resume = %v, %v", resumed, err)
	}
	if _, err := helper.Resume(t.Context(), testOwner, recovered); err != nil {
		t.Fatalf("resume must recover a failed status write: %v", err)
	}
	if annotations := getManaged(t, client, testName).GetAnnotations(); annotations[PausedAnnotation] != "" || annotations[OwnerAnnotation] != "" || annotations["example.com/keep"] != "yes" {
		t.Fatalf("unexpected annotations after resume: %v", annotations)
	}
	if getManaged(t, client, testHuman).GetAnnotations()[PausedAnnotation] != annotationTrue {
		t.Fatal("human pause was changed")
	}
	if mutations(client) != 2 {
		t.Fatalf("idempotent retry mutated resources: %d actions", mutations(client))
	}
}

func TestResumeRefusesMissingForeignAndReplacedObjects(t *testing.T) {
	for _, scenario := range []string{testMissing, "foreign", testHuman, testReplacementUID} {
		t.Run(scenario, func(t *testing.T) {
			object := managed(testUsers, testName, testOriginalUID)
			ref := reference(testUsers, object)
			objects := []runtime.Object{object}
			switch scenario {
			case testMissing:
				objects = nil
			case "foreign":
				object.SetAnnotations(map[string]string{PausedAnnotation: annotationTrue, OwnerAnnotation: "other-bootstrap"})
			case testHuman:
				object.SetAnnotations(map[string]string{PausedAnnotation: annotationTrue})
			case testReplacementUID:
				object.SetUID(testReplacementUID)
				object.SetAnnotations(map[string]string{PausedAnnotation: annotationTrue, OwnerAnnotation: testOwner})
			}
			helper, client := testClient(t, objects...)
			if _, err := helper.Resume(t.Context(), testOwner, []Reference{ref}); err == nil {
				t.Fatal("unsafe resumption accepted")
			}
			if mutations(client) != 0 {
				t.Fatal("unsafe resumption mutated an object")
			}
		})
	}
}

func TestPausePatchRejectsConcurrentReplacement(t *testing.T) {
	object := managed(testUsers, testName, testOriginalUID)
	helper, client := testClient(t, object)
	client.PrependReactor("patch", testUsers, func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := string(action.(clienttesting.PatchAction).GetPatch())
		if !strings.Contains(patch, "/metadata/uid") || !strings.Contains(patch, "/metadata/resourceVersion") {
			t.Fatal("atomic identity/version tests are missing")
		}
		replacement := object.DeepCopy()
		replacement.SetUID(testReplacementUID)
		if err := client.Tracker().Update(testGVR(testUsers), replacement, ""); err != nil {
			t.Fatal(err)
		}
		return false, nil, nil
	})
	paused, err := helper.Pause(t.Context(), testOwner, []Reference{reference(testUsers, object)})
	if err == nil || len(paused) != 0 {
		t.Fatalf("concurrent replacement accepted: %v, %v", paused, err)
	}
	if getManaged(t, client, testName).GetAnnotations()[PausedAnnotation] != "" {
		t.Fatal("replacement was paused")
	}
}

func TestReadinessRequiresEveryExactObjectAndBothConditions(t *testing.T) {
	for _, scenario := range []string{"ready", "ready-only", "synced-only", "false", testMissing, testReplacementUID, "terminating"} {
		t.Run(scenario, func(t *testing.T) {
			object := managed(testUsers, testName, testOriginalUID)
			ref := reference(testUsers, object)
			objects := []runtime.Object{object}
			switch scenario {
			case "ready-only":
				setConditions(object, true, false)
			case "synced-only":
				setConditions(object, false, true)
			case "false":
				object.Object[statusField] = map[string]any{"conditions": []any{map[string]any{testConditionType: "Ready", statusField: "False"}}}
			case testMissing:
				objects = nil
			case testReplacementUID:
				object.SetUID(testReplacementUID)
			case "terminating":
				now := metav1.Now()
				object.SetDeletionTimestamp(&now)
			}
			helper, _ := testClient(t, objects...)
			pending, err := helper.Ready(t.Context(), []Reference{ref})
			if err != nil || (len(pending) == 0) != (scenario == "ready") {
				t.Fatalf("pending = %v, error = %v", pending, err)
			}
		})
	}
}

func TestEmptyAndInvalidSelectionsFailClosed(t *testing.T) {
	helper, _ := testClient(t)
	if _, err := helper.Select(t.Context(), []string{testUsers}, nil); !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty API list: %v", err)
	}
	if _, err := helper.Select(t.Context(), nil, nil); !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty kinds: %v", err)
	}
	if _, err := helper.Ready(t.Context(), nil); !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty expected set: %v", err)
	}
	if _, err := helper.Pause(t.Context(), testOwner, nil); !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty pause set: %v", err)
	}
	if _, err := helper.Resume(t.Context(), testOwner, nil); !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty resume set: %v", err)
	}
	if _, done, err := helper.Recreate(t.Context(), nil, nil, RecreateOptions{}); !errors.Is(err, ErrEmptySelection) || done {
		t.Fatalf("empty recreation set: done=%t, %v", done, err)
	}
	if _, err := helper.Select(t.Context(), []string{"users/status"}, nil); err == nil {
		t.Fatal("subresource accepted as managed resource")
	}
}

func TestAPIErrorsAndPartialPauseArePreserved(t *testing.T) {
	a := managed(testUsers, "a", "uid-a")
	b := managed(testUsers, "b", "uid-b")
	helper, client := testClient(t, a, b)
	failure := apierrors.NewServiceUnavailable("temporarily unavailable")
	client.PrependReactor("patch", testUsers, func(action clienttesting.Action) (bool, runtime.Object, error) {
		return action.(clienttesting.PatchAction).GetName() == "b", nil, failure
	})
	paused, err := helper.Pause(t.Context(), testOwner, []Reference{reference(testUsers, a), reference(testUsers, b)})
	if !errors.Is(err, failure) || len(paused) != 1 || paused[0].Name != "a" {
		t.Fatalf("partial pause/error lost: %v, %v", paused, err)
	}
	client.PrependReactor("get", testUsers, func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	if _, err := helper.Ready(t.Context(), paused); !errors.Is(err, failure) {
		t.Fatalf("API error disguised as missing/pending: %v", err)
	}
	client.PrependReactor("list", testUsers, func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	if _, err := helper.Select(t.Context(), []string{testUsers}, nil); !errors.Is(err, failure) {
		t.Fatalf("API list error was ignored: %v", err)
	}
}

func testClient(t *testing.T, objects ...runtime.Object) (*Client, *fake.FakeDynamicClient) {
	t.Helper()
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		testGVR(testUsers): "UserList", testGVR(testGrants): "GrantList", testGVR("databases"): "DatabaseList",
	}, objects...)
	helper, err := New(client, testGroup, testVersion)
	if err != nil {
		t.Fatal(err)
	}
	return helper, client
}

func managed(kind, name string, uid types.UID) *unstructured.Unstructured {
	kinds := map[string]string{testUsers: "User", testGrants: "Grant", "databases": "Database"}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": testGroup + "/" + testVersion, "kind": kinds[kind],
		"metadata": map[string]any{"name": name, "uid": string(uid), "resourceVersion": "1"},
		"spec":     map[string]any{"deletionPolicy": "Orphan"},
	}}
	object.SetLabels(map[string]string{testLabel: "example"})
	setConditions(object, true, true)
	return object
}

func setConditions(object *unstructured.Unstructured, ready, synced bool) {
	conditions := make([]any, 0, 2)
	if ready {
		conditions = append(conditions, map[string]any{testConditionType: "Ready", statusField: "True"})
	}
	if synced {
		conditions = append(conditions, map[string]any{testConditionType: "Synced", statusField: "True"})
	}
	object.Object[statusField] = map[string]any{"conditions": conditions}
}

func reference(kind string, object *unstructured.Unstructured) Reference {
	return Reference{Kind: kind, Name: object.GetName(), UID: object.GetUID()}
}

func testGVR(kind string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: testGroup, Version: testVersion, Resource: kind}
}

func getManaged(t *testing.T, client *fake.FakeDynamicClient, name string) *unstructured.Unstructured {
	t.Helper()
	object, err := client.Resource(testGVR(testUsers)).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func mutations(client *fake.FakeDynamicClient) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" || action.GetVerb() == "delete" || action.GetVerb() == "update" {
			count++
		}
	}
	return count
}
