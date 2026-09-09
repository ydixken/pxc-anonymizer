// Package crossplane coordinates cluster-scoped managed resources without importing provider types.
package crossplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	PausedAnnotation = "crossplane.io/paused"
	OwnerAnnotation  = "pxc-anonymizer.io/bootstrap-pause-owner"
	annotationTrue   = "true"
	statusField      = "status"
)

var ErrEmptySelection = errors.New("crossplane selection must not be empty")

type Reference struct {
	Kind string
	Name string
	UID  types.UID
}

type Client struct {
	dynamic dynamic.Interface
	gv      schema.GroupVersion
}

func New(client dynamic.Interface, group, version string) (*Client, error) {
	if client == nil || len(validation.IsDNS1123Subdomain(group)) != 0 ||
		version == "" || strings.Contains(version, "/") {
		return nil, errors.New("crossplane client requires a client, API group and version")
	}
	return &Client{dynamic: client, gv: schema.GroupVersion{Group: group, Version: version}}, nil
}

func (c *Client) Select(ctx context.Context, kinds []string, selector *metav1.LabelSelector) ([]Reference, error) {
	if len(kinds) == 0 {
		return nil, ErrEmptySelection
	}
	labelSelector := ""
	if selector != nil {
		parsed, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return nil, fmt.Errorf("crossplane selector: %w", err)
		}
		labelSelector = parsed.String()
	}
	refs := make([]Reference, 0)
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if err := validateKind(kind); err != nil {
			return nil, err
		}
		if seen[kind] {
			return nil, fmt.Errorf("duplicate crossplane kind %q", kind)
		}
		seen[kind] = true
		objects, err := c.resource(kind).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return nil, fmt.Errorf("list crossplane %s: %w", kind, err)
		}
		slices.SortFunc(objects.Items, func(a, b unstructured.Unstructured) int {
			return strings.Compare(a.GetName(), b.GetName())
		})
		for _, object := range objects.Items {
			refs = append(refs, Reference{Kind: kind, Name: object.GetName(), UID: object.GetUID()})
		}
	}
	return refs, validateRefs(refs)
}

// Pause records our token with the pause so a failed status write can be recovered on retry.
func (c *Client) Pause(ctx context.Context, owner string, refs []Reference) ([]Reference, error) {
	if err := validateOperation(owner, refs); err != nil {
		return nil, err
	}
	paused := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		object, err := c.getOriginal(ctx, ref)
		if err != nil {
			return paused, err
		}
		annotations := object.GetAnnotations()
		if annotations[PausedAnnotation] == annotationTrue {
			if annotations[OwnerAnnotation] == owner {
				paused = append(paused, ref)
			}
			continue
		}
		if token := annotations[OwnerAnnotation]; token != "" && token != owner {
			return paused, ownershipError(ref)
		}
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[PausedAnnotation] = annotationTrue
		annotations[OwnerAnnotation] = owner
		if _, err := c.patch(ctx, ref.Kind, object, "/metadata/annotations", annotations); err != nil {
			return paused, err
		}
		paused = append(paused, ref)
	}
	return paused, nil
}

// Resume accepts only durable paused records; selection must never be repeated to authorize resumption.
func (c *Client) Resume(ctx context.Context, owner string, recorded []Reference) ([]Reference, error) {
	if err := validateOperation(owner, recorded); err != nil {
		return nil, err
	}
	resumed := make([]Reference, 0, len(recorded))
	for _, ref := range recorded {
		object, err := c.getOriginal(ctx, ref)
		if err != nil {
			return resumed, err
		}
		annotations := object.GetAnnotations()
		if annotations[OwnerAnnotation] == "" && annotations[PausedAnnotation] != annotationTrue {
			resumed = append(resumed, ref)
			continue
		}
		if annotations[OwnerAnnotation] != owner {
			return resumed, ownershipError(ref)
		}
		delete(annotations, PausedAnnotation)
		delete(annotations, OwnerAnnotation)
		if _, err := c.patch(ctx, ref.Kind, object, "/metadata/annotations", annotations); err != nil {
			return resumed, err
		}
		resumed = append(resumed, ref)
	}
	return resumed, nil
}

// Ready keeps missing and replaced objects pending rather than counting whatever a list happens to return.
func (c *Client) Ready(ctx context.Context, refs []Reference) ([]Reference, error) {
	if err := validateRefs(refs); err != nil {
		return nil, err
	}
	pending := make([]Reference, 0)
	for _, ref := range refs {
		object, err := c.resource(ref.Kind).Get(ctx, ref.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			pending = append(pending, ref)
			continue
		}
		if err != nil {
			return pending, fmt.Errorf("get crossplane %s/%s: %w", ref.Kind, ref.Name, err)
		}
		if object.GetUID() != ref.UID || !isReady(object) {
			pending = append(pending, ref)
		}
	}
	return pending, nil
}

func (c *Client) resource(kind string) dynamic.ResourceInterface {
	return c.dynamic.Resource(c.gv.WithResource(kind))
}

func (c *Client) getOriginal(ctx context.Context, ref Reference) (*unstructured.Unstructured, error) {
	object, err := c.resource(ref.Kind).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get crossplane %s/%s: %w", ref.Kind, ref.Name, err)
	}
	if object.GetUID() != ref.UID {
		return nil, fmt.Errorf("crossplane %s/%s UID changed", ref.Kind, ref.Name)
	}
	return object, nil
}

// JSON Patch tests keep a concurrent owner, finalizer, or object replacement from being overwritten.
type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (c *Client) patch(ctx context.Context, kind string, object *unstructured.Unstructured, path string, value any) (*unstructured.Unstructured, error) {
	ops := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: object.GetUID()}}
	if version := object.GetResourceVersion(); version != "" {
		ops = append(ops, patchOperation{Op: "test", Path: "/metadata/resourceVersion", Value: version})
	}
	ops = append(ops, patchOperation{Op: "add", Path: path, Value: value})
	data, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("encode crossplane patch: %w", err)
	}
	updated, err := c.resource(kind).Patch(ctx, object.GetName(), types.JSONPatchType, data, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch crossplane %s/%s: %w", kind, object.GetName(), err)
	}
	return updated, nil
}

func validateKind(kind string) error {
	if len(validation.IsDNS1035Label(kind)) != 0 {
		return fmt.Errorf("invalid crossplane resource kind %q", kind)
	}
	return nil
}

func validateRefs(refs []Reference) error {
	if len(refs) == 0 {
		return ErrEmptySelection
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if err := validateKind(ref.Kind); err != nil {
			return err
		}
		key := ref.Kind + "/" + ref.Name
		if ref.Name == "" || ref.UID == "" || seen[key] {
			return fmt.Errorf("crossplane reference %s requires a unique name and UID", key)
		}
		seen[key] = true
	}
	return nil
}

func validateOperation(owner string, refs []Reference) error {
	if owner == "" {
		return errors.New("crossplane pause owner must not be empty")
	}
	return validateRefs(refs)
}

func ownershipError(ref Reference) error {
	return fmt.Errorf("crossplane %s/%s pause is not owned by this Bootstrap", ref.Kind, ref.Name)
}

func isReady(object *unstructured.Unstructured) bool {
	if object.GetDeletionTimestamp() != nil {
		return false
	}
	conditions, found, err := unstructured.NestedSlice(object.Object, statusField, "conditions")
	if err != nil || !found {
		return false
	}
	ready, synced := false, false
	for _, condition := range conditions {
		value, ok := condition.(map[string]any)
		if !ok {
			return false
		}
		switch value["type"] {
		case "Ready":
			ready = value[statusField] == "True"
		case "Synced":
			synced = value[statusField] == "True"
		}
	}
	return ready && synced
}
