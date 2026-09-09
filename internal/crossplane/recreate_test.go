package crossplane

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
)

func TestRecreationCheckpointsDeletionAndNeverDeletesReplacement(t *testing.T) {
	object := managed(testUsers, testName, testOriginalUID)
	helper, client := testClient(t, object)
	refs := []Reference{reference(testUsers, object)}
	state, done, err := helper.Recreate(t.Context(), refs, nil, RecreateOptions{})
	if err != nil || done || len(state) != 1 || state[0].UID != testOriginalUID || mutations(client) != 0 {
		t.Fatalf("initial durable checkpoint must precede effects: %v, %t, %v", state, done, err)
	}
	client.PrependReactor("delete", testUsers, func(action clienttesting.Action) (bool, runtime.Object, error) {
		preconditions := action.(clienttesting.DeleteAction).GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != testOriginalUID ||
			preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "1" {
			t.Fatal("delete lacks original UID and resource-version preconditions")
		}
		return false, nil, nil
	})
	state, done, err = helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || done || state[0].DeletionObserved {
		t.Fatalf("delete must not claim observed disappearance: %v, %t, %v", state, done, err)
	}
	state, done, err = helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || done || !state[0].DeletionObserved {
		t.Fatalf("absence must wait for replacement: %v, %t, %v", state, done, err)
	}
	replacement := managed(testUsers, testName, testReplacementUID)
	setConditions(replacement, true, false)
	if err := client.Tracker().Create(testGVR(testUsers), replacement, ""); err != nil {
		t.Fatal(err)
	}
	state, done, err = helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || done {
		t.Fatalf("replacement must be Ready and Synced: %v, %t, %v", state, done, err)
	}
	setConditions(replacement, true, true)
	if err := client.Tracker().Update(testGVR(testUsers), replacement, ""); err != nil {
		t.Fatal(err)
	}
	state, done, err = helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || !done || !state[0].Complete {
		t.Fatalf("ready replacement did not complete: %v, %t, %v", state, done, err)
	}
	if _, done, err := helper.Recreate(t.Context(), refs, state, RecreateOptions{}); err != nil || !done || mutations(client) != 1 {
		t.Fatalf("completed retry mutated replacement: done=%t err=%v mutations=%d", done, err, mutations(client))
	}
}

func TestRecreationCompletesEachKindBeforeDeletingTheNext(t *testing.T) {
	user := managed(testUsers, testName, testOriginalUID)
	grant := managed(testGrants, "example-grant", "grant-uid")
	helper, client := testClient(t, user, grant)
	refs := []Reference{reference(testUsers, user), reference(testGrants, grant)}
	state := []Recreation{{Reference: refs[0]}, {Reference: refs[1]}}
	state, done, err := helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || done || mutations(client) != 1 {
		t.Fatalf("expected only first kind deletion: done=%t error=%v actions=%d", done, err, mutations(client))
	}
	if _, err := client.Tracker().Get(testGVR(testGrants), "", grant.GetName()); err != nil {
		t.Fatalf("later kind disappeared before replacement became ready: %v", err)
	}
	user.SetUID(testReplacementUID)
	if err := client.Tracker().Create(testGVR(testUsers), user, ""); err != nil {
		t.Fatal(err)
	}
	state, done, err = helper.Recreate(t.Context(), refs, state, RecreateOptions{})
	if err != nil || done || !state[0].Complete || state[1].Complete || mutations(client) != 2 {
		t.Fatalf("next kind was not advanced after ready replacement: %v, %t, %v", state, done, err)
	}
}

func TestRecreationRecognizesReplacementAfterCrashWithoutNotFoundPoll(t *testing.T) {
	object := managed(testUsers, testName, testReplacementUID)
	helper, client := testClient(t, object)
	ref := Reference{Kind: testUsers, Name: testName, UID: testOriginalUID}
	state := []Recreation{{Reference: ref}}
	next, done, err := helper.Recreate(t.Context(), []Reference{ref}, state, RecreateOptions{})
	if err != nil || !done || !next[0].DeletionObserved || mutations(client) != 0 {
		t.Fatalf("new UID must prove old deletion without another delete: %v, %t, %v", next, done, err)
	}
}

func TestRecreationWaitsForOriginalDeletionDespiteUnchangedCount(t *testing.T) {
	object := managed(testUsers, testName, testOriginalUID)
	now := metav1.Now()
	object.SetDeletionTimestamp(&now)
	object.SetFinalizers([]string{testFinalizer})
	helper, client := testClient(t, object)
	ref := reference(testUsers, object)
	state := []Recreation{{Reference: ref}}
	next, done, err := helper.Recreate(t.Context(), []Reference{ref}, state, RecreateOptions{})
	if err != nil || done || next[0].DeletionObserved || mutations(client) != 0 {
		t.Fatalf("terminating original incorrectly completed: %v, %t, %v", next, done, err)
	}
}

func TestDatabaseGuardRunsBeforeAnyFinalizerOrDeleteMutation(t *testing.T) {
	object := managed("databases", "example-database", "database-uid")
	object.Object["spec"] = map[string]any{"deletionPolicy": "Delete"}
	object.SetFinalizers([]string{testFinalizer})
	helper, client := testClient(t, object)
	ref := reference("databases", object)
	for _, state := range [][]Recreation{nil, {{Reference: ref}}} {
		if _, done, err := helper.Recreate(t.Context(), []Reference{ref}, state, RecreateOptions{RemoveFinalizers: true}); err == nil || done {
			t.Fatal("unsafe database recreation accepted")
		}
	}
	if mutations(client) != 0 {
		t.Fatal("database guard ran after a destructive action")
	}
}

func TestFinalizerRemovalRequiresExplicitOptIn(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "removed"}[remove], func(t *testing.T) {
			object := managed(testUsers, testName, testOriginalUID)
			now := metav1.Now()
			object.SetDeletionTimestamp(&now)
			object.SetFinalizers([]string{testFinalizer})
			helper, client := testClient(t, object)
			ref := reference(testUsers, object)
			_, done, err := helper.Recreate(t.Context(), []Reference{ref}, []Recreation{{Reference: ref}}, RecreateOptions{RemoveFinalizers: remove})
			if err != nil || done {
				t.Fatalf("unexpected recreation result: %t, %v", done, err)
			}
			if (len(getManaged(t, client, testName).GetFinalizers()) == 0) != remove {
				t.Fatal("finalizer removal ignored the explicit option")
			}
		})
	}
}

func TestRecreationHonorsKindOrderAndPropagatesDeleteErrors(t *testing.T) {
	user := managed(testUsers, testName, testOriginalUID)
	grant := managed(testGrants, "example-grant", "grant-uid")
	helper, client := testClient(t, user, grant)
	refs := []Reference{reference(testUsers, user), reference(testGrants, grant)}
	state, _, err := helper.Recreate(t.Context(), refs, nil, RecreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failure := apierrors.NewServiceUnavailable("temporarily unavailable")
	client.PrependReactor("delete", testUsers, func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	if _, done, err := helper.Recreate(t.Context(), refs, state, RecreateOptions{}); !errors.Is(err, failure) || done {
		t.Fatalf("delete error was ignored: done=%t error=%v", done, err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == testGrants {
			t.Fatal("later kind deleted before first kind completed")
		}
	}
}

func TestRecreationNoWaitStillRequiresDeletionAndRejectsInvalidCheckpoint(t *testing.T) {
	object := managed(testUsers, testName, testOriginalUID)
	helper, client := testClient(t, object)
	ref := reference(testUsers, object)
	wait := false
	options := RecreateOptions{WaitForRecreation: &wait}
	state := []Recreation{{Reference: ref}}
	next, done, err := helper.Recreate(t.Context(), []Reference{ref}, state, options)
	if err != nil || done {
		t.Fatalf("delete alone cannot prove disappearance: %t, %v", done, err)
	}
	if _, done, err := helper.Recreate(t.Context(), []Reference{ref}, next, options); err != nil || !done {
		t.Fatalf("observed absence should complete with wait disabled: %t, %v", done, err)
	}
	client.ClearActions()
	state[0].UID = "wrong"
	if _, done, err := helper.Recreate(t.Context(), []Reference{ref}, state, options); err == nil || done || mutations(client) != 0 {
		t.Fatalf("mismatched checkpoint accepted: %t, %v", done, err)
	}
}
