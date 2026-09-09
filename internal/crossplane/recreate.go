package crossplane

import (
	"context"
	"errors"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Recreation struct {
	Reference
	DeletionObserved bool
	Complete         bool
}

type RecreateOptions struct {
	RemoveFinalizers  bool
	WaitForRecreation *bool
}

// Recreate returns an initial checkpoint without effects; persist every returned checkpoint before calling again.
// Keep refs and the client's API group/version fixed for the operation, including after process restarts.
func (c *Client) Recreate(ctx context.Context, refs []Reference, progress []Recreation, options RecreateOptions) ([]Recreation, bool, error) {
	if err := validateRefs(refs); err != nil {
		return progress, false, err
	}
	if len(progress) == 0 {
		return c.initializeRecreation(ctx, refs)
	}
	if err := validateProgress(refs, progress); err != nil {
		return progress, false, err
	}
	next := slices.Clone(progress)
	pendingKind := ""
	for i := range next {
		if pendingKind != "" && next[i].Kind != pendingKind {
			break
		}
		if next[i].Complete {
			continue
		}
		updated, err := c.recreateOne(ctx, next[i], options)
		next[i] = updated
		if err != nil {
			return next, false, err
		}
		if !updated.Complete {
			pendingKind = updated.Kind
		}
	}
	complete := !slices.ContainsFunc(next, func(item Recreation) bool { return !item.Complete })
	return next, complete, nil
}

func (c *Client) initializeRecreation(ctx context.Context, refs []Reference) ([]Recreation, bool, error) {
	progress := make([]Recreation, 0, len(refs))
	for _, ref := range refs {
		object, err := c.getOriginal(ctx, ref)
		if err != nil {
			return nil, false, err
		}
		if err := allowRecreation(ref, object); err != nil {
			return nil, false, err
		}
		progress = append(progress, Recreation{Reference: ref})
	}
	return progress, false, nil
}

func (c *Client) recreateOne(ctx context.Context, state Recreation, options RecreateOptions) (Recreation, error) {
	object, err := c.resource(state.Kind).Get(ctx, state.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		state.DeletionObserved = true
		state.Complete = !waitForRecreation(options)
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("get crossplane %s/%s: %w", state.Kind, state.Name, err)
	}
	if object.GetUID() != state.UID {
		if object.GetUID() == "" {
			return state, errors.New("replacement crossplane object has no UID")
		}
		state.DeletionObserved = true
		state.Complete = !waitForRecreation(options) || isReady(object)
		return state, nil
	}
	if state.DeletionObserved {
		return state, fmt.Errorf("crossplane %s/%s original UID remains after recorded deletion", state.Kind, state.Name)
	}
	if err := allowRecreation(state.Reference, object); err != nil {
		return state, err
	}
	if options.RemoveFinalizers && len(object.GetFinalizers()) != 0 {
		object, err = c.patch(ctx, state.Kind, object, "/metadata/finalizers", []string{})
		if err != nil {
			return state, err
		}
	}
	if object.GetDeletionTimestamp() == nil {
		version := object.GetResourceVersion()
		err = c.resource(state.Kind).Delete(ctx, state.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &state.UID, ResourceVersion: &version},
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return state, fmt.Errorf("delete crossplane %s/%s: %w", state.Kind, state.Name, err)
		}
	}
	return state, nil
}

func validateProgress(refs []Reference, progress []Recreation) error {
	if len(refs) != len(progress) {
		return errors.New("crossplane recreation checkpoint does not match the expected set")
	}
	for i, ref := range refs {
		if progress[i].Reference != ref || (progress[i].Complete && !progress[i].DeletionObserved) {
			return errors.New("crossplane recreation checkpoint identity or deletion state is invalid")
		}
	}
	return nil
}

func allowRecreation(ref Reference, object *unstructured.Unstructured) error {
	if ref.Kind != "databases" {
		return nil
	}
	policy, _, err := unstructured.NestedString(object.Object, "spec", "deletionPolicy")
	if err != nil || policy != "Orphan" {
		return fmt.Errorf("crossplane database %s recreation requires deletionPolicy Orphan", ref.Name)
	}
	return nil
}

func waitForRecreation(options RecreateOptions) bool {
	return options.WaitForRecreation == nil || *options.WaitForRecreation
}
